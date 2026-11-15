package gitcontrol

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

const controlVersion = "superbench.git-control.v2"
const legacyControlVersion = "superbench.git-control.v1"
const maxIntent = 4096

var controlID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var hostID = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Runner owns durable Git-control scopes. Its caller serializes this store's
// state and mirror operations, including Recover, so recovery never fences a
// legitimate concurrent worker. State belongs outside bounded checkout data.
type Runner struct {
	State, Root string
	// Timeout bounds one control command; zero uses the two-minute default.
	Timeout time.Duration
}
type runnerContextKey struct{}

func WithRunner(ctx context.Context, r *Runner) context.Context {
	return context.WithValue(ctx, runnerContextKey{}, r)
}
func Run(ctx context.Context, cmd *exec.Cmd) error {
	if r, _ := ctx.Value(runnerContextKey{}).(*Runner); r != nil {
		return r.Run(ctx, cmd)
	}
	if err := operationcontrol.Effect(ctx, cmd.Start); err != nil {
		return err
	}
	return cmd.Wait()
}

type controlIntent struct {
	Version, Root, ID, HostID, BootID string
	OperationID, OperationEpoch       string `json:",omitempty"`
}

func (j controlIntent) execution() *execution.Execution {
	return &execution.Execution{Root: j.Root, ID: j.ID, HostID: j.HostID, BootID: j.BootID, Phase: "starting"}
}
func controlDir(state string, create bool) (string, error) {
	if !filepath.IsAbs(state) || filepath.Clean(state) != state {
		return "", errors.New("Git control state must be canonical and absolute")
	}
	info, e := os.Lstat(state)
	if e != nil || !info.IsDir() {
		return "", errors.New("Git control state must be a physical directory")
	}
	actual, e := filepath.EvalSymlinks(state)
	if e != nil {
		return "", errors.New("Git control state ancestors unavailable")
	}
	state = actual
	dir := filepath.Join(state, "git-controls")
	if create {
		e = os.Mkdir(dir, 0700)
		if e != nil && !os.IsExist(e) {
			return "", errors.New("Git control journal directory unavailable")
		}
		if e = syncDirectory(state); e != nil {
			return "", errors.New("Git control journal directory is not durable")
		}
	}
	info, e = os.Lstat(dir)
	if os.IsNotExist(e) && !create {
		return dir, nil
	}
	if e != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return "", errors.New("Git control journal directory must be private")
	}
	return dir, nil
}
func syncDirectory(dir string) error {
	f, e := os.Open(dir)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func saveControl(dir string, j controlIntent) error {
	raw, e := json.Marshal(j)
	if e != nil {
		return e
	}
	f, e := os.OpenFile(filepath.Join(dir, j.ID+".json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return errors.New("Git control intent already exists or is unavailable")
	}
	_, e = f.Write(raw)
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if e == nil {
		e = syncDirectory(dir)
	}
	if e != nil {
		return errors.New("Git control intent persistence failed")
	}
	return nil
}
func removeControl(dir, id string) error {
	if !controlID.MatchString(id) {
		return errors.New("invalid Git control intent identity")
	}
	if e := os.Remove(filepath.Join(dir, id+".json")); e != nil {
		return e
	}
	return syncDirectory(dir)
}

func (r Runner) deadline(ctx context.Context) (context.Context, context.CancelFunc, error) {
	limit := r.Timeout
	if limit == 0 {
		limit = 2 * time.Minute
	}
	if limit < 0 || limit > time.Hour {
		return nil, nil, errors.New("Git control timeout must be between zero (default) and one hour")
	}
	bounded, cancel := context.WithTimeout(ctx, limit)
	return bounded, cancel, nil
}

// Run starts only after its cgroup identity is durable, and never reports success
// until the process and every cgroup descendant have been fenced. A retained
// journal always requires recovery; elapsed time never transfers a bench lease.
func (r Runner) Run(ctx context.Context, cmd *exec.Cmd) error {
	ctx, cancel, err := r.deadline(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	if e := ctx.Err(); e != nil {
		return e
	}
	if cmd == nil {
		return errors.New("Git control command is missing")
	}
	if e := validateRoot(r.Root); e != nil {
		return e
	}
	dir, e := operationcontrol.Value(ctx, func() (string, error) { return controlDir(r.State, true) })
	if e != nil {
		return e
	}
	var rawID [16]byte
	if _, e = rand.Read(rawID[:]); e != nil {
		return e
	}
	id := hex.EncodeToString(rawID[:])
	host := execution.Host{}
	identity, e := operationcontrol.Value(ctx, func() (execution.Identity, error) { return host.Create(r.Root, id) })
	if e != nil {
		return e
	}
	j := controlIntent{Version: controlVersion, Root: r.Root, ID: id, HostID: identity.HostID, BootID: identity.BootID}
	if op, ok := operationcontrol.FromContext(ctx); ok {
		j.OperationID = op.Target.ID()
		j.OperationEpoch = op.Epoch
	}
	scope := j.execution()
	if e = saveControl(dir, j); e != nil {
		_ = host.Fence(scope)
		return e
	}
	if cmd.WaitDelay == 0 || cmd.WaitDelay > 2*time.Second {
		cmd.WaitDelay = 2 * time.Second
	}
	e = operationcontrol.Effect(ctx, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return host.Start(cmd, scope)
	})
	if e != nil {
		if fenceErr := host.Fence(scope); fenceErr != nil {
			return errors.Join(e, fmt.Errorf("Git control fence failed; intent retained: %w", fenceErr))
		}
		return errors.Join(e, removeControl(dir, id))
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		if fenceErr := host.Fence(scope); fenceErr != nil {
			// Kill only the exact command leader we started. This does not prove
			// descendants stopped: retain the intent even when its pipes/reaper
			// finish, and never report this path as a successful fence.
			_ = cmd.Process.Kill()
			var reapErr error
			select {
			case reapErr = <-done:
			case <-time.After(2100 * time.Millisecond):
				reapErr = errors.New("Git control leader reap timed out; intent retained")
			}
			return errors.Join(ctx.Err(), reapErr, fmt.Errorf("Git control fence failed; intent retained: %w", fenceErr))
		}
		select {
		case waitErr = <-done:
		case <-time.After(2100 * time.Millisecond):
			waitErr = errors.New("Git control process reap timed out")
		}
		return errors.Join(ctx.Err(), waitErr, removeControl(dir, id))
	}
	if e = host.Fence(scope); e != nil {
		return errors.Join(waitErr, fmt.Errorf("Git control fence failed; intent retained: %w", e))
	}
	return errors.Join(waitErr, removeControl(dir, id))
}

func readControl(dir, name string) (controlIntent, error) {
	var j controlIntent
	id := strings.TrimSuffix(name, ".json")
	f, e := os.OpenFile(filepath.Join(dir, name), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return j, errors.New("Git control intent unavailable")
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > maxIntent {
		return j, errors.New("Git control intent must be a bounded private file")
	}
	raw, e := io.ReadAll(io.LimitReader(f, maxIntent+1))
	if e != nil || len(raw) > maxIntent {
		return j, errors.New("Git control intent exceeds bounds")
	}
	// A flat string-only object has one interpretation: duplicate keys and every
	// unknown field are refused rather than silently keeping the last value.
	d := json.NewDecoder(bytes.NewReader(raw))
	token, e := d.Token()
	if e != nil || token != json.Delim('{') {
		return j, errors.New("invalid Git control intent")
	}
	seen := map[string]bool{}
	for d.More() {
		k, e := d.Token()
		key, ok := k.(string)
		if e != nil || !ok || seen[key] {
			return j, errors.New("ambiguous Git control intent")
		}
		switch key {
		case "Version", "Root", "ID", "HostID", "BootID", "OperationID", "OperationEpoch":
		default:
			return j, errors.New("unknown Git control intent field")
		}
		seen[key] = true
		var v string
		if d.Decode(&v) != nil {
			return j, errors.New("invalid Git control intent field")
		}
	}
	if _, e = d.Token(); e != nil {
		return j, errors.New("invalid Git control intent")
	}
	if _, e = d.Token(); e != io.EOF {
		return j, errors.New("invalid Git control intent")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&j) != nil {
		return j, errors.New("invalid Git control intent fields")
	}
	if (j.Version != controlVersion && j.Version != legacyControlVersion) || j.ID != id || !controlID.MatchString(j.ID) || !hostID.MatchString(j.HostID) || len(j.BootID) < 1 || len(j.BootID) > 128 || strings.ContainsAny(j.BootID, "\x00\r\n\t ") {
		return j, errors.New("invalid Git control intent identity")
	}
	if j.Version == legacyControlVersion && (seen["OperationID"] || seen["OperationEpoch"]) || (j.OperationID != "" || j.OperationEpoch != "") && (!hostID.MatchString(j.OperationID) || !controlID.MatchString(j.OperationEpoch)) || seen["OperationID"] != seen["OperationEpoch"] {
		return j, errors.New("invalid Git operation provenance")
	}
	return j, nil
}

// Recover fences only validated private intents while the caller holds the
// store lock. It never clears a bench lease or guesses that an old writer ended.
func Recover(state string) error {
	if !filepath.IsAbs(state) || filepath.Clean(state) != state {
		return errors.New("Git control state must be canonical and absolute")
	}
	if _, e := os.Lstat(state); os.IsNotExist(e) {
		return nil
	}
	dir, e := controlDir(state, false)
	if e != nil {
		return e
	}
	f, e := os.Open(dir)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	defer f.Close()
	var intents []controlIntent
	for {
		entries, e := f.ReadDir(128)
		if e != nil && e != io.EOF {
			return e
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".json") || !controlID.MatchString(strings.TrimSuffix(name, ".json")) {
				continue
			}
			if len(intents) >= 1000 {
				return errors.New("Git control recovery exceeds intent limit")
			}
			j, e := readControl(dir, name)
			if e != nil {
				return e
			}
			intents = append(intents, j)
		}
		if e == io.EOF {
			break
		}
	}
	// Validate all roots before any journal can authorize a filesystem operation.
	// Even after a reboot, a missing historical root fails closed until the
	// operator restores a validated cgroup location; we do not duplicate the
	// execution backend's host/boot authority to bypass this filesystem check.
	for _, j := range intents {
		if e = validateRoot(j.Root); e != nil {
			return e
		}
	}
	host := execution.Host{}
	for _, j := range intents {
		if e = host.Fence(j.execution()); e != nil {
			return fmt.Errorf("Git control recovery fence failed; intent retained: %w", e)
		}
		if e = removeControl(dir, j.ID); e != nil {
			return e
		}
	}
	return nil
}
