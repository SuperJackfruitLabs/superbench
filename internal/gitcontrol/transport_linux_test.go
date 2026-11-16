//go:build linux

package gitcontrol

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadlaunch"
)

type payloadTrialHost struct {
	intent         *operationcontrol.Intent
	t              *testing.T
	state          string
	gate           *atomic.Bool
	mu             sync.Mutex
	process        *os.Process
	starts, fences int
	failFence      error
}

func (h *payloadTrialHost) Create(_ string, id string) (execution.Identity, error) {
	if !h.gate.Load() {
		return execution.Identity{}, errors.New("scope outside operation gate")
	}
	return execution.Identity{HostID: strings.Repeat("2", 64), BootID: "00000000-0000-0000-0000-000000000001"}, nil
}
func (h *payloadTrialHost) Start(cmd *exec.Cmd, e *execution.Execution) error {
	if !h.gate.Load() {
		return errors.New("start outside operation gate")
	}
	raw, err := os.ReadFile(filepath.Join(h.state, "git-controls", e.ID+".json"))
	if err != nil {
		return err
	}
	var intent controlIntent
	if json.Unmarshal(raw, &intent) != nil || intent.ID != e.ID || intent.HostID != e.HostID || intent.BootID != e.BootID {
		return errors.New("start before durable scope/operation intent")
	}
	if h.intent != nil && (intent.OperationID != h.intent.Target.ID() || intent.OperationEpoch != h.intent.Epoch) || h.intent == nil && (intent.OperationID != "" || intent.OperationEpoch != "") {
		return errors.New("scope intent bound to wrong operation")
	}
	if cmd.Path != "/usr/bin/sudo" {
		return errors.New("fixed helper invocation lost")
	}
	// Test-only substitution after the real command guard. This verifies actual
	// Bridge pumps/journaling, not sudo installation or kernel cgroup isolation.
	cmd.Path = os.Args[0]
	cmd.Args = []string{os.Args[0], "-test.run=^TestGitPayloadOwnedChild$"}
	cmd.Env = append(cmd.Env, "SUPERBENCH_GIT_CHILD=1")
	if err := cmd.Start(); err != nil {
		return err
	}
	h.mu.Lock()
	h.process = cmd.Process
	h.starts++
	h.mu.Unlock()
	return nil
}
func (h *payloadTrialHost) Fence(*execution.Execution) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fences++
	if h.failFence != nil {
		return h.failFence
	}
	if h.process != nil {
		if err := h.process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
	}
	return nil
}

type payloadTrial struct {
	afterDrain   func()
	afterPrepare func()
	failPump     bool
	t            *testing.T
	gate         *atomic.Bool
	out          *bytes.Buffer
	afterWait    func()
	prepareError error
	checkError   error
	nilTransport bool
}
type brokenPayloadSink struct{}

func (brokenPayloadSink) Write([]byte) (int, error) { return 0, io.ErrShortBuffer }

type trialTransport struct {
	*payloadlaunch.Bridge
	owner *payloadTrial
	guard payloadlaunch.CommandGuard
}

func (p *payloadTrial) Prepare(cmd *exec.Cmd, e *execution.Execution) (Transport, error) {
	if p.prepareError != nil {
		return nil, p.prepareError
	}
	if p.nilTransport {
		return nil, nil
	}
	if p.failPump {
		cmd.Stdout = brokenPayloadSink{}
	}
	b, err := payloadlaunch.Prepare(cmd, payloadlaunch.Target{Key: "fixture", LayoutID: strings.Repeat("3", 32), PolicyDigest: strings.Repeat("4", 64), Kind: "git"}, e, payloadlaunch.GitInput{Bytes: []byte("finite-pack"), Limit: 64})
	if err != nil {
		return nil, err
	}
	if p.afterPrepare != nil {
		p.afterPrepare()
	}
	return &trialTransport{Bridge: b, owner: p, guard: payloadlaunch.GuardCommand(cmd)}, nil
}
func (b *trialTransport) Check() error {
	if !b.owner.gate.Load() {
		return errors.New("transport check outside operation gate")
	}
	if b.owner.checkError != nil {
		return b.owner.checkError
	}
	return b.guard.Check()
}
func (b *trialTransport) StartIO() error {
	if !b.owner.gate.Load() {
		return errors.New("input delivered outside operation gate")
	}
	return b.Bridge.StartIO()
}
func (b *trialTransport) WaitLeader() error {
	err := b.Bridge.WaitLeader()
	if b.owner.afterWait != nil {
		b.owner.afterWait()
	}
	return err
}
func (b *trialTransport) Drain(ctx context.Context, f interface {
	Fence(*execution.Execution) error
}) error {
	deadline, ok := ctx.Deadline()
	if ctx.Err() != nil || !ok || time.Until(deadline) > 5*time.Second {
		return errors.New("cleanup context not independent and bounded")
	}
	err := b.Bridge.Drain(ctx, f)
	if b.owner.afterDrain != nil {
		b.owner.afterDrain()
	}
	return err
}

func TestGitPayloadOwnedChild(t *testing.T) {
	if os.Getenv("SUPERBENCH_GIT_CHILD") != "1" {
		return
	}
	var head [4]byte
	if _, err := io.ReadFull(os.Stdin, head[:]); err != nil {
		t.Fatal(err)
	}
	size := binary.BigEndian.Uint32(head[:])
	if size == 0 || size > payloadexec.MaxRequest {
		t.Fatal("invalid frame bound")
	}
	frame := make([]byte, size)
	if _, err := io.ReadFull(os.Stdin, frame); err != nil {
		t.Fatal(err)
	}
	request, err := payloadexec.ParseRequest(frame)
	if err != nil || request.Kind != "git" || len(request.Arguments) != 2 || request.Arguments[1] != "fixture" {
		t.Fatalf("wrong frame: %+v %v", request, err)
	}
	input, err := io.ReadAll(io.LimitReader(os.Stdin, 65))
	if err != nil || string(input) != "finite-pack" {
		t.Fatalf("wrong finite input: %q %v", input, err)
	}
	if _, err := io.WriteString(os.Stdout, "private-pack\n"); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestGitPayloadRealTransportJournalsGatesFencesAndDrains(t *testing.T) {
	refusal := errors.New("changed registration")
	for _, kind := range []string{"success", "prepare refusal", "missing transport", "check refusal", "revoked before launch", "pump failure", "standalone cancel before publication", "cancel before publication", "epoch changed before publication", "fence failure"} {
		t.Run(kind, func(t *testing.T) {
			state := canonicalTemp(t)
			var held atomic.Bool
			var revoked atomic.Bool
			intent := operationcontrol.Intent{Target: operationcontrol.Target{UID: 1001, RequestID: "git_operation", Digest: strings.Repeat("5", 64)}, Key: "fixture", Operation: "run", Epoch: strings.Repeat("6", 32)}
			watchdog, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			runCtx, cancel := context.WithCancel(watchdog)
			defer cancel()
			ctx, err := operationcontrol.WithScope(runCtx, intent, func(fn func() error) error {
				if revoked.Load() {
					return operationcontrol.ErrAuthority
				}
				held.Store(true)
				defer held.Store(false)
				return fn()
			})
			if err != nil {
				t.Fatal(err)
			}
			h := &payloadTrialHost{t: t, state: state, gate: &held, intent: &intent}
			var output bytes.Buffer
			p := &payloadTrial{t: t, gate: &held, out: &output}
			switch kind {
			case "prepare refusal":
				p.prepareError = refusal
			case "missing transport":
				p.nilTransport = true
			case "check refusal":
				p.checkError = refusal
			case "pump failure":
				p.failPump = true
			case "revoked before launch":
				p.afterPrepare = func() { revoked.Store(true) }
			case "standalone cancel before publication":
				ctx = runCtx
				held.Store(true)
				h.intent = nil
				p.afterDrain = cancel
			case "cancel before publication":
				p.afterWait = cancel
			case "epoch changed before publication":
				p.afterWait = func() { revoked.Store(true) }
			case "fence failure":
				h.failFence = refusal
			}
			cmd := exec.Command("git", "fixture")
			cmd.Env = []string{}
			cmd.Stdout = &output
			cmd.Stderr = io.Discard
			err = (Runner{State: state, Root: state}).runPayload(ctx, cmd, p, h)
			if watchdog.Err() != nil {
				t.Fatalf("owned transport exceeded watchdog: %v", watchdog.Err())
			}
			if kind == "success" {
				if err != nil || output.String() != "private-pack\n" {
					t.Fatalf("real transport failed: %q %v", output.String(), err)
				}
			} else if err == nil {
				t.Fatal("refusal published success")
			}
			if (kind == "cancel before publication" || kind == "standalone cancel before publication") && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
			if (kind == "epoch changed before publication" || kind == "revoked before launch") && !errors.Is(err, operationcontrol.ErrAuthority) {
				t.Fatalf("lost epoch refusal: %v", err)
			}
			if kind == "prepare refusal" || kind == "check refusal" || kind == "fence failure" {
				if !errors.Is(err, refusal) {
					t.Fatalf("lost exact refusal: %v", err)
				}
			}
			if kind == "missing transport" && (err == nil || !strings.Contains(err.Error(), "Git payload transport missing")) {
				t.Fatalf("lost missing transport refusal: %v", err)
			}
			if kind == "pump failure" && !errors.Is(err, io.ErrShortBuffer) {
				t.Fatalf("lost pump failure: %v", err)
			}
			entries, readErr := os.ReadDir(filepath.Join(state, "git-controls"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if kind == "fence failure" || kind == "pump failure" {
				if len(entries) != 1 {
					t.Fatalf("uncertain scope lost intent: %v", entries)
				}
			} else if len(entries) != 0 {
				t.Fatalf("quiescent intent not removed: %v", entries)
			}
			h.mu.Lock()
			starts, fences := h.starts, h.fences
			h.mu.Unlock()
			if kind == "prepare refusal" || kind == "check refusal" || kind == "missing transport" || kind == "revoked before launch" {
				if starts != 0 {
					t.Fatal("refused transport started child")
				}
			} else if starts != 1 {
				t.Fatal("missing actual child")
			}
			if fences < 1 {
				t.Fatal("scope was never fenced")
			}
		})
	}
}
