//go:build linux

package stageexec_test

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/output"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadlaunch"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/stageexec"
)

type realBridge struct {
	*payloadlaunch.Bridge
	held *atomic.Bool
}

func (b *realBridge) Check() error {
	if !b.held.Load() {
		return errors.New("bridge check outside atomic gate")
	}
	return nil
}
func (b *realBridge) StartIO() error {
	if !b.held.Load() {
		return errors.New("frame outside atomic gate")
	}
	return b.Bridge.StartIO()
}
func (b *realBridge) Drain(ctx context.Context, f interface {
	Fence(*execution.Execution) error
}) error {
	if ctx.Err() != nil {
		return fmt.Errorf("cleanup inherited cancellation: %w", ctx.Err())
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 5*time.Second || ctx.Value(bridgeContextKey{}) != "preserved" {
		return errors.New("cleanup lost bound or context value")
	}
	return b.Bridge.Drain(ctx, f)
}

type bridgeContextKey struct{}

type bridgeFence struct {
	mu     sync.Mutex
	holder *exec.Cmd
	leader *exec.Cmd
	calls  int
}

func (f *bridgeFence) Fence(*execution.Execution) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.leader != nil {
		_ = f.leader.Process.Kill()
		f.leader = nil // WaitLeader owns reaping.
	}
	if f.holder == nil {
		return nil
	}
	holder := f.holder
	f.holder = nil
	if err := holder.Process.Kill(); err != nil {
		return err
	}
	var exit *exec.ExitError
	if err := holder.Wait(); !errors.As(err, &exit) {
		return fmt.Errorf("owned setsid holder was not reaped: %w", err)
	}
	return nil
}

var bridgePumpFault = errors.New("real bridge integration sink fault")

type brokenBridgeSink struct{}

func (brokenBridgeSink) Write([]byte) (int, error) { return 0, bridgePumpFault }

func TestRealBridgeRunnerFencesSetsidPipeHolderBeforeDrainAndSeal(t *testing.T) {
	runRealBridge(t, true, false, false, 0)
}
func TestRealBridgeRunnerPumpFailureRevokesAndNeverSeals(t *testing.T) {
	runRealBridge(t, false, true, false, 0)
}

func TestRealBridgeRunnerCancellationStillDrainsAndSeals(t *testing.T) {
	runRealBridge(t, false, false, true, 0)
}

// Only this test binary substitutes the fixed sudo command AFTER asserting its
// exact invocation. This proves real Bridge/Runner composition, not sudo/root
// credential drop, installed registration authority or kernel cgroup fencing.
func TestRealBridgeOpaqueResultSeparatedAfterFence(t *testing.T) {
	runRealBridge(t, true, false, false, 128)
}
func TestRealBridgeOpaqueResultOverflowCannotPublish(t *testing.T) {
	runRealBridge(t, false, false, false, 4)
}

func runRealBridge(t *testing.T, withHolder, failPump, cancelRun bool, resultLimit uint64) {
	t.Helper()
	dir := t.TempDir()
	scope := &execution.Execution{ID: strings.Repeat("1", 32), Root: dir, HostID: strings.Repeat("2", 64), BootID: "00000000-0000-0000-0000-000000000001", Phase: "starting", Log: filepath.Join(dir, "run.log"), LogLimitBytes: 4096}
	var held, sealed, cancelled atomic.Bool
	var cancelCount, sealCount atomic.Int32
	watchdog, watchdogCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer watchdogCancel()
	ctx, cancel := context.WithCancel(context.WithValue(watchdog, bridgeContextKey{}, "preserved"))
	defer cancel()
	ready := filepath.Join(dir, "ready")
	fence := &bridgeFence{}
	t.Cleanup(func() { _ = fence.Fence(scope) })
	runner := stageexec.Runner{Backend: fence}
	runner.Prepare = func(cmd *exec.Cmd, e *execution.Execution) (stageexec.Transport, error) {
		if failPump {
			cmd.Stdout = brokenBridgeSink{}
			cmd.Stderr = cmd.Stdout
		}
		bridge, err := payloadlaunch.Prepare(cmd, payloadlaunch.Target{Key: "fixture", LayoutID: strings.Repeat("3", 32), Member: "a", PolicyDigest: strings.Repeat("4", 64), Kind: "payload"}, e, payloadlaunch.GitInput{})
		if err != nil {
			return nil, err
		}
		if cmd.Path != "/usr/bin/sudo" || !reflect.DeepEqual(cmd.Args, []string{"sudo", "-n", "--", payloadlaunch.Helper}) || len(cmd.ExtraFiles) != 0 {
			_ = bridge.Close()
			return nil, errors.New("fixed helper invocation changed")
		}
		if withHolder {
			holder := exec.Command(os.Args[0], "-test.run=^TestBridgeRunnerOwnedProcess$", "-test.timeout=30s")
			holder.Env = []string{"SUPERBENCH_BRIDGE_RUNNER_ROLE=holder"}
			holder.Stdout = cmd.Stdout
			holder.Stderr = cmd.Stderr
			holder.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			if err := holder.Start(); err != nil {
				_ = bridge.Close()
				return nil, err
			}
			fence.holder = holder
			sid, _, errno := syscall.Syscall(syscall.SYS_GETSID, uintptr(holder.Process.Pid), 0, 0)
			if errno != 0 || sid != uintptr(holder.Process.Pid) {
				_ = bridge.Close()
				return nil, errors.New("owned holder did not setsid")
			}
		}
		return &realBridge{Bridge: bridge, held: &held}, nil
	}
	runner.StartPrepared = func(_ context.Context, cmd *exec.Cmd, _ *execution.Execution, launch stageexec.Launch) error {
		held.Store(true)
		defer held.Store(false)
		err := launch(func() error {
			cmd.Path = os.Args[0]
			cmd.Args = []string{os.Args[0], "-test.run=^TestBridgeRunnerOwnedProcess$", "-test.timeout=30s"}
			role := "leader"
			if cancelRun {
				role = "cancelled-leader"
			}
			cmd.Env = append(cmd.Env, "SUPERBENCH_BRIDGE_RUNNER_ROLE="+role)
			if cancelRun {
				cmd.Env = append(cmd.Env, "SUPERBENCH_BRIDGE_READY="+ready)
			}
			if err := cmd.Start(); err != nil {
				return err
			}
			if cancelRun {
				fence.mu.Lock()
				fence.leader = cmd
				fence.mu.Unlock()
			}
			return nil
		})
		if err == nil && cancelRun {
			for {
				if _, readyErr := os.Stat(ready); readyErr == nil {
					cancel()
					break
				}
				if watchdog.Err() != nil {
					return watchdog.Err()
				}
				time.Sleep(time.Millisecond)
			}
		}
		return err
	}
	runner.Stop = func() (*execution.Execution, error) {
		if err := fence.Fence(scope); err != nil {
			return nil, err
		}
		copy := *scope
		copy.Phase = "stopped"
		return &copy, nil
	}
	runner.Cancel = func() { cancelled.Store(true); cancelCount.Add(1) }
	runner.Seal = func(*output.Capture, *int, bool) error { sealed.Store(true); sealCount.Add(1); return nil }
	result, err := runner.Run(ctx, stageexec.Plan{Execution: scope, Dir: dir, Args: []string{"payload", "known-argument"}, Environment: []string{"LANG=C"}, ResultLimitBytes: resultLimit})
	if watchdog.Err() != nil {
		t.Fatalf("leader waited for held output instead of reaching fence: %v", watchdog.Err())
	}
	if resultLimit != 0 && resultLimit < uint64(len("real-bridge-output\n")) {
		if !errors.Is(err, stageexec.ErrResultOverflow) || result == nil || result.Output != nil || sealed.Load() || !cancelled.Load() {
			t.Fatalf("overflow published/sealed result: %+v %v sealed=%v cancelled=%v", result, err, sealed.Load(), cancelled.Load())
		}
	} else if failPump {
		if !errors.Is(err, bridgePumpFault) || sealed.Load() || !cancelled.Load() {
			t.Fatalf("real pump fault did not retain/revoke unsealed owner: %v sealed=%v cancelled=%v", err, sealed.Load(), cancelled.Load())
		}
	} else {
		var exitErr *exec.ExitError
		goodExit := err == nil && result != nil && result.ExitCode == 0
		if cancelRun {
			goodExit = errors.As(err, &exitErr) && ctx.Err() == context.Canceled && cancelCount.Load() == 1
		}
		if !goodExit || result == nil || !sealed.Load() || sealCount.Load() != 1 {
			t.Fatalf("real bridge composition failed: %v %+v sealed=%v", err, result, sealed.Load())
		}
		raw, readErr := os.ReadFile(scope.Log)
		wantLog := "real-bridge-output\n"
		if resultLimit != 0 {
			wantLog = ""
			if string(result.Output) != "real-bridge-output\n" {
				t.Fatalf("lost separate result: %q", result.Output)
			}
		}
		if readErr != nil || string(raw) != wantLog {
			t.Fatalf("capture writer close lost actual bytes: %q %v", raw, readErr)
		}
	}
	fence.mu.Lock()
	defer fence.mu.Unlock()
	if fence.holder != nil || fence.calls < 2 {
		t.Fatalf("Stop and Drain did not independently fence: holder=%v calls=%d", fence.holder, fence.calls)
	}
}

func TestBridgeRunnerOwnedProcess(t *testing.T) {
	role := os.Getenv("SUPERBENCH_BRIDGE_RUNNER_ROLE")
	if role == "" {
		return
	}
	if role == "holder" {
		for {
			time.Sleep(time.Hour)
		}
	}
	if role != "leader" && role != "cancelled-leader" {
		t.Fatal("unknown owned subprocess role")
	}
	var header [4]byte
	if _, err := io.ReadFull(os.Stdin, header[:]); err != nil {
		t.Fatal(err)
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > payloadexec.MaxRequest {
		t.Fatal("invalid frame size")
	}
	raw := make([]byte, int(size))
	if _, err := io.ReadFull(os.Stdin, raw); err != nil {
		t.Fatal(err)
	}
	request, err := payloadexec.ParseRequest(raw)
	if err != nil || request.Kind != "payload" || !reflect.DeepEqual(request.Arguments, []string{"payload", "known-argument"}) {
		t.Fatalf("frame changed: %v", err)
	}
	remaining, err := io.ReadAll(io.LimitReader(os.Stdin, 1))
	if err != nil || len(remaining) != 0 {
		t.Fatal("payload input was not EOF")
	}
	if _, err := io.WriteString(os.Stdout, "real-bridge-output\n"); err != nil {
		t.Fatal(err)
	}
	if role == "cancelled-leader" {
		if err := os.WriteFile(os.Getenv("SUPERBENCH_BRIDGE_READY"), []byte("ready"), 0600); err != nil {
			t.Fatal(err)
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	os.Exit(0)
}
