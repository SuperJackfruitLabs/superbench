//go:build linux

// Package payloadlaunch prepares the fixed managed helper's transport. It does
// not establish registration authority, start processes, install policy or sudo.
package payloadlaunch

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
)

var ErrTransport = errors.New("managed payload transport refused")

const Helper = "/usr/local/libexec/bench-payload"

// Target must come from the caller's current locked registration/owner check.
// These logical fields cannot select a UID, helper path or host policy.
type Target struct {
	Key, LayoutID, Member, PolicyDigest, Kind string
	Root                                      bool
}

// GitInput is finite owned input, never an arbitrary potentially infinite reader.
// Limit must be the caller's existing approved input/storage bound, not a wire
// supplied enlargement. Payload invocations require the zero value (EOF).
type GitInput struct {
	Bytes []byte
	Limit uint64
}
type Fencer interface {
	Fence(*execution.Execution) error
}

type pump struct {
	reader *os.File
	sink   io.Writer
}
type Bridge struct {
	cmd                     *exec.Cmd
	scope                   execution.Execution
	input                   []byte
	inputWriter             *os.File
	childEnds               []*os.File
	pumps                   []pump
	owned                   []*os.File
	failures                chan error
	done                    chan struct{}
	mu                      sync.Mutex
	started, closed, waited bool
	waitErr                 error
	waitOnce, closeOnce     sync.Once
	firstErr                error
	closeErr                error
}

// Prepare mutates the SAME unstarted Cmd, preserving its context/Cancel and
// WaitDelay. Input must be explicit; a preexisting Cmd.Stdin is not adopted.
// Sinks remain daemon-side only. Non-file sinks must be trusted bounded writers.
func Prepare(cmd *exec.Cmd, t Target, e *execution.Execution, input GitInput) (*Bridge, error) {
	return prepare(cmd, t, e, input, nil, false)
}

// PayloadInput is explicit finite opaque stdin. Limit comes from trusted caller
// policy, never from payload data; it cannot exceed the helper's fixed maximum.
type PayloadInput struct {
	Bytes []byte
	Limit uint64
	// ReportExec asks the helper to prefix stdout with payloadexec.ExecReached.
	ReportExec bool
}

// PreparePayloadInput is proposed internal transport only, not a public RUN API.
// It never adopts Cmd.Stdin or parses the bytes. Empty input preserves EOF.
func PreparePayloadInput(cmd *exec.Cmd, t Target, e *execution.Execution, input PayloadInput) (*Bridge, error) {
	if t.Kind != "payload" || input.Limit == 0 || input.Limit > payloadexec.MaxPayloadInput || uint64(len(input.Bytes)) > input.Limit {
		return nil, ErrTransport
	}
	return prepare(cmd, t, e, GitInput{}, bytes.Clone(input.Bytes), input.ReportExec)
}
func prepare(cmd *exec.Cmd, t Target, e *execution.Execution, input GitInput, payload []byte, report bool) (b *Bridge, err error) {
	if cmd == nil || cmd.Process != nil || cmd.SysProcAttr != nil || len(cmd.ExtraFiles) != 0 || cmd.Stdin != nil || e == nil || e.Phase != "starting" || !filepath.IsAbs(e.Root) || filepath.Clean(e.Root) != e.Root || e.Root == "/" {
		return nil, ErrTransport
	}
	if t.Kind != "git" && (len(input.Bytes) != 0 || input.Limit != 0) || uint64(len(input.Bytes)) > input.Limit {
		return nil, ErrTransport
	}
	r := payloadexec.Request{Version: payloadexec.Version, Key: t.Key, LayoutID: t.LayoutID, Member: t.Member, PolicyDigest: t.PolicyDigest, Kind: t.Kind, Root: t.Root, Scope: payloadexec.Scope{ID: e.ID, HostID: e.HostID, BootID: e.BootID}, Arguments: append([]string(nil), cmd.Args...), Input: payload, ReportExec: report}
	raw, err := r.Bytes()
	if err != nil {
		return nil, errors.Join(ErrTransport, err)
	}
	ambient, err := payloadexec.AmbientEnvironment(cmd.Env)
	if err != nil {
		return nil, errors.Join(ErrTransport, err)
	}
	env := append([]string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}, ambient...)
	b = &Bridge{cmd: cmd, scope: *e, failures: make(chan error, 1), done: make(chan struct{})}
	owned := b
	defer func() {
		if err != nil {
			_ = owned.Close()
			b = nil
		}
	}()
	prefix := make([]byte, 4+len(raw))
	binary.BigEndian.PutUint32(prefix, uint32(len(raw)))
	copy(prefix[4:], raw)
	b.input = append(prefix, bytes.Clone(input.Bytes)...)
	inR, inW, err := b.pipe()
	if err != nil {
		return nil, err
	}
	b.inputWriter = inW
	b.childEnds = append(b.childEnds, inR)
	stdout, stderr := cmd.Stdout, cmd.Stderr
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	outR, outW, err := b.pipe()
	if err != nil {
		return nil, err
	}
	sink, err := b.sink(stdout)
	if err != nil {
		return nil, err
	}
	b.pumps = append(b.pumps, pump{outR, sink})
	b.childEnds = append(b.childEnds, outW)
	errW := outW
	if !sameWriter(stdout, stderr) {
		var errR *os.File
		errR, errW, err = b.pipe()
		if err != nil {
			return nil, err
		}
		sink, err = b.sink(stderr)
		if err != nil {
			return nil, err
		}
		b.pumps = append(b.pumps, pump{errR, sink})
		b.childEnds = append(b.childEnds, errW)
	}
	cmd.Path = "/usr/bin/sudo"
	cmd.Args = []string{"sudo", "-n", "--", Helper}
	cmd.Dir = "/"
	cmd.Env = env
	cmd.Err = nil
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
	return b, nil
}
func sameWriter(a, b io.Writer) bool {
	return a != nil && b != nil && reflect.TypeOf(a) == reflect.TypeOf(b) && reflect.TypeOf(a).Comparable() && a == b
}
func (b *Bridge) pipe() (*os.File, *os.File, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, errors.Join(ErrTransport, err)
	}
	b.owned = append(b.owned, r, w)
	return r, w, nil
}
func (b *Bridge) sink(w io.Writer) (io.Writer, error) {
	if f, ok := w.(*os.File); ok {
		fd, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_DUPFD_CLOEXEC, 3)
		if errno != 0 {
			return nil, errors.Join(ErrTransport, errno)
		}
		dup := os.NewFile(fd, "payload transport sink")
		b.owned = append(b.owned, dup)
		return dup, nil
	}
	return w, nil
}
func (b *Bridge) fail(err error) {
	if err == nil {
		return
	}
	failure := errors.Join(ErrTransport, err)
	b.mu.Lock()
	if b.firstErr == nil {
		b.firstErr = failure
	}
	b.mu.Unlock()
	select {
	case b.failures <- failure:
	default:
	}
}

// Failures must join the caller's exact-scope cancellation/fencing monitor. A
// failure notification alone is never stopped, drained, sealed or successful.
func (b *Bridge) Failures() <-chan error { return b.failures }

// StartIO follows successful actual backend.Start, while launch authority remains
// held. The child blocks on its frame until these daemon-owned pumps start.
func (b *Bridge) StartIO() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.started || b.cmd.Process == nil {
		return ErrTransport
	}
	b.started = true
	for _, f := range b.childEnds {
		_ = f.Close()
	}
	var wg sync.WaitGroup
	wg.Add(len(b.pumps) + 1)
	go func() {
		defer wg.Done()
		_, err := io.Copy(b.inputWriter, bytes.NewReader(b.input))
		b.fail(err)
		_ = b.inputWriter.Close()
	}()
	for _, p := range b.pumps {
		p := p
		go func() {
			defer wg.Done()
			buf := make([]byte, 32<<10)
			failed := false
			for {
				n, readErr := p.reader.Read(buf)
				if n > 0 && !failed {
					written, err := p.sink.Write(buf[:n])
					if err == nil && written != n {
						err = io.ErrShortWrite
					}
					if err != nil {
						b.fail(err)
						failed = true
					}
				}
				if readErr != nil {
					if readErr != io.EOF {
						b.fail(readErr)
					}
					return
				}
			}
		}()
	}
	go func() { wg.Wait(); close(b.done) }()
	return nil
}

// WaitLeader never waits for output pumps: descendant-held stdout cannot prevent
// the caller from reaching its actual scope fence. It remains usable after
// Close: releasing IO must never prevent reaping an already-started process.
func (b *Bridge) WaitLeader() error {
	b.mu.Lock()
	valid := b.cmd.Process != nil
	b.mu.Unlock()
	if !valid {
		return ErrTransport
	}
	b.waitOnce.Do(func() { b.waitErr = b.cmd.Wait(); b.mu.Lock(); b.waited = true; b.mu.Unlock() })
	return b.waitErr
}

// Drain independently invokes the supplied actual backend's fence on the pinned
// scope, then joins pumps. No bool/caller assertion can stand in for that fence.
// Callers must not seal history or publish stdout before this succeeds.
func (b *Bridge) Drain(ctx context.Context, backend Fencer) error {
	b.mu.Lock()
	valid := b.started && b.waited && !b.closed
	b.mu.Unlock()
	if !valid || backend == nil || ctx == nil {
		return ErrTransport
	}
	scope := b.scope
	if err := backend.Fence(&scope); err != nil {
		return errors.Join(ErrTransport, err)
	}
	select {
	case <-ctx.Done():
		return errors.Join(ErrTransport, ctx.Err())
	case <-b.done:
	}
	b.mu.Lock()
	failure := b.firstErr
	b.mu.Unlock()
	return errors.Join(failure, b.Close())
}

// Close releases ONLY owned pipes and atomic duplicates, never caller sinks.
// On uncertain fencing this is cleanup, never a seal/quiescence proof.
func (b *Bridge) Close() error {
	b.closeOnce.Do(func() {
		b.mu.Lock()
		b.closed = true
		b.mu.Unlock()
		for _, f := range b.owned {
			if err := f.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
				b.closeErr = errors.Join(b.closeErr, err)
			}
		}
	})
	return b.closeErr
}
