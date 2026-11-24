//go:build linux

package remotesession

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotessh"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotestate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/serviceidentity"
)

const helper = "/usr/local/libexec/bench-payload"

// Start owns fds and closes them on every path: once sent, only the helper
// holds the session's ends. The helper is born in a.Execution's existing
// scope (clone3 into the cgroup; a missing scope is never created), and the
// session is started only when the helper reports ExecReached.
func (Installed) Start(ctx context.Context, a remotestate.Admission, spec remotessh.SessionSpec, fds []*os.File) (_ remotessh.Process, err error) {
	defer closeAll(fds)
	authority, err := serviceidentity.ResolveService()
	if err != nil {
		return nil, err
	}
	policy, err := authority.Policy()
	if err != nil {
		return nil, err
	}
	registry, err := serviceidentity.ForPolicy(policy)
	if err != nil {
		return nil, err
	}
	r, err := sessionRequest(a, spec, registry.PolicySHA256)
	if err != nil {
		return nil, err
	}
	body, err := r.Bytes()
	if err != nil || len(fds) != map[bool]int{true: 1, false: 3}[spec.PTY] {
		return nil, errors.Join(ErrSpec, err)
	}
	ambient, err := payloadexec.AmbientEnvironment(os.Environ())
	if err != nil {
		return nil, errors.Join(ErrSpec, err)
	}
	pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	control, stdin := os.NewFile(uintptr(pair[0]), "session control"), os.NewFile(uintptr(pair[1]), "session helper stdin")
	defer control.Close()
	outR, outW, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		closeAll([]*os.File{stdin, outR, outW})
		return nil, err
	}
	cmd := &exec.Cmd{Path: "/usr/bin/sudo", Args: []string{"sudo", "-n", "--", helper}, Dir: "/", Env: append([]string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}, ambient...), Stdin: stdin, Stdout: outW, Stderr: errW}
	err = execution.Host{}.Start(cmd, &a.Execution)
	closeAll([]*os.File{stdin, outW, errW})
	if err != nil {
		closeAll([]*os.File{outR, errR})
		return nil, err
	}
	p := &process{cmd: cmd, done: make(chan struct{})}
	stderr := make(chan []byte, 1)
	go func() { _, _ = io.Copy(io.Discard, outR); _ = outR.Close() }()
	go func() {
		raw, _ := io.ReadAll(io.LimitReader(errR, 4<<10))
		_, _ = io.Copy(io.Discard, errR)
		_ = errR.Close()
		stderr <- raw
	}()
	go p.reap(stderr)
	if err := handshake(ctx, int(control.Fd()), body, fds, time.Now().Add(StartTimeout)); err != nil {
		p.Terminate()
		_, _, _ = p.Wait()
		if errors.Is(err, ErrRefused) && ctx.Err() == nil {
			return nil, errors.Join(refusal(p.stderr), err)
		}
		return nil, err
	}
	return p, nil
}

// process is the sudo process; sudo relays signals to the helper's exec
// target and exits with its status, or dies of its signal.
type process struct {
	cmd    *exec.Cmd
	done   chan struct{}
	once   sync.Once
	code   int
	signal string
	err    error
	stderr []byte
}

func (p *process) reap(stderr <-chan []byte) {
	err := p.cmd.Wait()
	p.stderr = <-stderr
	p.code, p.err = -1, err
	if state := p.cmd.ProcessState; state != nil {
		status, _ := state.Sys().(syscall.WaitStatus)
		if status.Signaled() {
			p.signal, p.err = signalName(status.Signal()), nil
			if p.signal == "" {
				p.code = 128 + int(status.Signal())
			}
		} else if status.Exited() {
			p.code, p.err = status.ExitStatus(), nil
		}
	}
	close(p.done)
}

func (p *process) Wait() (int, string, error) {
	<-p.done
	return p.code, p.signal, p.err
}

func (p *process) Terminate() {
	p.once.Do(func() {
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
		go func() {
			select {
			case <-p.done:
			case <-time.After(5 * time.Second):
				_ = p.cmd.Process.Kill()
			}
		}()
	})
}

// signalName is the RFC 4254 exit-signal name, without "SIG", or "" for a
// signal RFC 4254 does not name (reported as exit status 128+n instead).
func signalName(s syscall.Signal) string {
	names := map[syscall.Signal]string{syscall.SIGABRT: "ABRT", syscall.SIGALRM: "ALRM", syscall.SIGFPE: "FPE", syscall.SIGHUP: "HUP", syscall.SIGILL: "ILL", syscall.SIGINT: "INT", syscall.SIGKILL: "KILL", syscall.SIGPIPE: "PIPE", syscall.SIGQUIT: "QUIT", syscall.SIGSEGV: "SEGV", syscall.SIGTERM: "TERM", syscall.SIGUSR1: "USR1", syscall.SIGUSR2: "USR2"}
	return names[s]
}
