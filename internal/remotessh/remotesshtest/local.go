// Package remotesshtest is a test-only launcher for remotessh: it runs
// /bin/sh as the calling user in the admission's root, and SFTP in process.
// It has no confinement of any kind and is never wired into benchd.
package remotesshtest

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotesftp"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotessh"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotestate"
)

// Local implements remotessh.Launcher: "/bin/sh -l" for a shell, "/bin/sh -c"
// for exec, each in its own process group (a session with the PTY as its
// controlling terminal), and remotesftp.Serve on the pipes for sftp.
type Local struct{}

var errSpec = errors.New("remotesshtest: invalid session")

func (Local) Start(ctx context.Context, a remotestate.Admission, spec remotessh.SessionSpec, fds []*os.File) (remotessh.Process, error) {
	if len(fds) != map[bool]int{true: 1, false: 3}[spec.PTY] || a.Root == "" {
		closeAll(fds)
		return nil, errSpec
	}
	if err := ctx.Err(); err != nil {
		closeAll(fds)
		return nil, err
	}
	if spec.Mode == "sftp" {
		return startSFTP(a.Root, fds)
	}
	args := []string{"/bin/sh", "-l"}
	switch spec.Mode {
	case "exec":
		args = []string{"/bin/sh", "-c", spec.Command}
	case "shell":
	default:
		closeAll(fds)
		return nil, errSpec
	}
	cmd := &exec.Cmd{Path: "/bin/sh", Args: args, Dir: a.Root, Env: []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + a.Root, "LANG=C"}}
	if spec.PTY {
		cmd.Env = append(cmd.Env, "TERM="+spec.Term)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = fds[0], fds[0], fds[0]
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	} else {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = fds[0], fds[1], fds[2]
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	err := cmd.Start()
	closeAll(fds)
	if err != nil {
		return nil, err
	}
	p := &process{done: make(chan struct{})}
	p.kill = func(sig syscall.Signal) { _ = syscall.Kill(-cmd.Process.Pid, sig) }
	go func() {
		err := cmd.Wait()
		p.code, p.err = -1, nil
		if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			p.signal = signalName(status.Signal())
			if p.signal == "" {
				p.code = 128 + int(status.Signal())
			}
		} else if cmd.ProcessState != nil {
			p.code = cmd.ProcessState.ExitCode()
		} else {
			p.err = err
		}
		p.mu.Lock()
		p.exited = true
		p.mu.Unlock()
		close(p.done)
	}()
	return p, nil
}

// startSFTP serves SFTP in process on the stdin and stdout pipes, rooted at
// root; stderr is unused. Terminate closes the pipes.
func startSFTP(root string, fds []*os.File) (remotessh.Process, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		closeAll(fds)
		return nil, err
	}
	_ = fds[2].Close()
	p := &process{done: make(chan struct{})}
	stream := &pipes{in: fds[0], out: fds[1]}
	p.kill = func(syscall.Signal) { _ = stream.Close() }
	go func() {
		err := remotesftp.Serve(stream, r)
		_ = stream.Close()
		_ = r.Close()
		p.code, p.err = 0, nil
		if err != nil {
			p.code = 1
		}
		p.mu.Lock()
		p.exited = true
		p.mu.Unlock()
		close(p.done)
	}()
	return p, nil
}

type pipes struct {
	in, out *os.File
	once    sync.Once
}

func (p *pipes) Read(b []byte) (int, error)  { return p.in.Read(b) }
func (p *pipes) Write(b []byte) (int, error) { return p.out.Write(b) }
func (p *pipes) Close() error {
	p.once.Do(func() { _ = p.in.Close(); _ = p.out.Close() })
	return nil
}

var _ io.ReadWriteCloser = (*pipes)(nil)

type process struct {
	done   chan struct{}
	kill   func(syscall.Signal)
	code   int
	signal string
	err    error
	once   sync.Once
	mu     sync.Mutex
	exited bool
}

func (p *process) Wait() (int, string, error) {
	<-p.done
	return p.code, p.signal, p.err
}

// Terminate signals the process group with SIGTERM, then SIGKILL after five
// seconds, and never after the leader was reaped.
func (p *process) Terminate() {
	p.once.Do(func() {
		p.signalUnlessExited(syscall.SIGTERM)
		go func() {
			select {
			case <-p.done:
			case <-time.After(5 * time.Second):
				p.signalUnlessExited(syscall.SIGKILL)
			}
		}()
	})
}

func (p *process) signalUnlessExited(sig syscall.Signal) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.exited {
		p.kill(sig)
	}
}

func signalName(s syscall.Signal) string {
	names := map[syscall.Signal]string{syscall.SIGABRT: "ABRT", syscall.SIGALRM: "ALRM", syscall.SIGFPE: "FPE", syscall.SIGHUP: "HUP", syscall.SIGILL: "ILL", syscall.SIGINT: "INT", syscall.SIGKILL: "KILL", syscall.SIGPIPE: "PIPE", syscall.SIGQUIT: "QUIT", syscall.SIGSEGV: "SEGV", syscall.SIGTERM: "TERM", syscall.SIGUSR1: "USR1", syscall.SIGUSR2: "USR2"}
	return names[s]
}

func closeAll(files []*os.File) {
	for _, f := range files {
		if f != nil {
			_ = f.Close()
		}
	}
}
