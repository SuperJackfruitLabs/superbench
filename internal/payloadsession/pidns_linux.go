//go:build linux

package payloadsession

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadwire"
)

// The session runs as two processes. The root stage (in the caller's PID
// namespace, inside the remote scope) forks once into a new PID namespace and
// then only relays signals and the exit status. Its child is the namespace's
// PID 1: it mounts a private /proc, takes the one credential drop, starts the
// fixed target and reaps every process in the namespace until the target
// exits. When PID 1 exits the kernel kills the rest of the namespace; when the
// root stage dies, PID 1 gets SIGKILL. Both stay in the scope, so a fence
// (cgroup.kill) still kills everything.
//
// PID 1 is this same executable, re-executed with no arguments. It is told
// apart only by facts the daemon cannot produce through sudo: it is PID 1
// and its fd 3 is a socket whose peer is root.
const (
	handoffFD     = 3
	controlFD     = 4
	maxHandoff    = 1 << 20
	terminateWait = 5 * time.Second
)

// Handoff carries what the namespace hides from PID 1 (the layout's paths)
// and what the root stage already decided and verified.
type Handoff struct {
	Path        string
	Argv, Env   []string
	PTY         bool
	Directory   string
	Device, Ino uint64
}

// Run is the root stage after every check: ready the descriptors, record the
// pinned workspace root's identity for PID 1, release every privileged
// descriptor, fork PID 1 and relay. It returns only on error.
func Run(fds []*os.File, pinned *os.File, h Handoff, uid, gid uint32, release func() error) error {
	if err := Prepare(fds, h.PTY, uid, gid); err != nil {
		return err
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(int(pinned.Fd()), &st); err != nil {
		return errors.Join(ErrSession, err)
	}
	h.Device, h.Ino = st.Dev, st.Ino
	if err := release(); err != nil {
		return err
	}
	pid, handoff, err := spawn(fds, 0, h)
	if err != nil {
		return err
	}
	return relay(pid, handoff)
}

// Child reports whether this process is the session's PID 1 stage.
func Child() bool {
	if os.Getpid() != 1 {
		return false
	}
	kind, err := syscall.GetsockoptInt(handoffFD, syscall.SOL_SOCKET, syscall.SO_TYPE)
	if err != nil || kind != syscall.SOCK_STREAM {
		return false
	}
	peer, err := syscall.GetsockoptUcred(handoffFD, syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	return err == nil && peer.Uid == 0
}

// spawn forks the PID 1 stage into a new PID namespace (one clone with
// CLONE_NEWPID, from the thread that owns the session's mount namespace) with
// the session's descriptors as fds 0-2, the handoff as 3 and the control socket
// as 4, and closes this process's copies. Every other descriptor here must
// already be close-on-exec. The caller then only calls relay.
func spawn(fds []*os.File, control int, h Handoff) (int, *os.File, error) {
	raw, err := json.Marshal(h)
	if err != nil || len(raw) > maxHandoff || len(fds) != 1 && len(fds) != 3 {
		return 0, nil, errors.Join(ErrSession, err)
	}
	pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return 0, nil, errors.Join(ErrSession, err)
	}
	parent, child := os.NewFile(uintptr(pair[0]), "session handoff"), os.NewFile(uintptr(pair[1]), "session handoff child")
	files := []uintptr{fds[0].Fd(), fds[min(1, len(fds)-1)].Fd(), fds[len(fds)-1].Fd(), child.Fd(), uintptr(control)}
	pid, err := syscall.ForkExec("/proc/self/exe", []string{"bench-payload"}, &syscall.ProcAttr{Dir: "/", Env: []string{}, Files: files, Sys: &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWPID, Pdeathsig: syscall.SIGKILL}})
	_ = child.Close()
	for _, f := range fds {
		_ = f.Close()
	}
	if err == nil {
		err = writeAll(int(parent.Fd()), binary.BigEndian.AppendUint32(nil, uint32(len(raw))), raw)
	}
	if err != nil {
		_ = parent.Close()
		return 0, nil, errors.Join(ErrSession, err)
	}
	return pid, parent, nil
}

// relay is all the root stage does after spawn: the control socket on fd 0
// becomes /dev/null, SIGTERM, SIGHUP, SIGINT and SIGQUIT go to PID 1, and the
// process exits with PID 1's exit code, or 128+signal. It returns only on error.
func relay(pid int, handoff *os.File) error {
	if err := null(0); err != nil {
		return err
	}
	forward(func(s syscall.Signal) { _ = syscall.Kill(pid, s) })
	for {
		var status syscall.WaitStatus
		if _, err := syscall.Wait4(pid, &status, 0, nil); err == syscall.EINTR {
			continue
		} else if err != nil {
			return errors.Join(ErrSession, err)
		}
		_ = handoff.Close()
		exit(status)
	}
}

// Begin is PID 1's root work: read the handoff and mount a fresh proc, which
// shows only this namespace's processes, over /proc. Only Child processes
// may call it.
func Begin() (Handoff, error) {
	var h Handoff
	if !Child() {
		return h, ErrSession
	}
	tv := syscall.NsecToTimeval(int64(30 * time.Second))
	if err := syscall.SetsockoptTimeval(handoffFD, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); err != nil {
		return h, errors.Join(ErrSession, err)
	}
	reader := fdReader(handoffFD)
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return h, errors.Join(ErrSession, err)
	}
	length := binary.BigEndian.Uint32(header[:])
	raw := make([]byte, min(length, maxHandoff+1))
	if _, err := io.ReadFull(reader, raw); err != nil || length > maxHandoff || json.Unmarshal(raw, &h) != nil {
		return h, errors.Join(ErrSession, err)
	}
	if h.Path != "/usr/bin/bash" && h.Path != "/usr/local/libexec/bench-sftp" || len(h.Argv) == 0 || !filepath.IsAbs(h.Directory) {
		return Handoff{}, ErrSession
	}
	if err := syscall.Mount("proc", "/proc", "proc", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, "hidepid=2"); err != nil {
		return Handoff{}, errors.Join(ErrSession, err)
	}
	return h, nil
}

// Init runs as the payload user after the credential drop. It never returns
// once the target has started: it reaps every process in the namespace and
// exits with the target's status. verify is the final credential and
// descriptor proof, run immediately before the target starts.
func Init(h Handoff, verify func() error) error {
	// The drop changed credentials, which clears the parent-death signal: set
	// it again, then make sure the root stage did not exit in between (it
	// holds the handoff open until it exits).
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_PDEATHSIG, uintptr(syscall.SIGKILL), 0); errno != 0 {
		return errors.Join(ErrSession, errno)
	}
	var probe [1]byte
	if n, _, err := syscall.Recvfrom(handoffFD, probe[:], syscall.MSG_PEEK|syscall.MSG_DONTWAIT); n == 0 && err == nil || n > 0 {
		return ErrSession
	}
	var cwd syscall.Stat_t
	if err := syscall.Chdir(h.Directory); err != nil {
		return errors.Join(ErrSession, err)
	}
	if err := syscall.Stat(".", &cwd); err != nil || cwd.Dev != h.Device || cwd.Ino != h.Ino {
		return errors.Join(ErrSession, err)
	}
	if err := verify(); err != nil {
		return err
	}
	os.Clearenv()
	signals := forward(nil)
	target, err := syscall.ForkExec(h.Path, h.Argv, &syscall.ProcAttr{Env: h.Env, Files: []uintptr{0, 1, 2}, Sys: &syscall.SysProcAttr{Setsid: true, Setctty: h.PTY, Ctty: 0}})
	if err != nil {
		_ = writeAll(controlFD, []byte(payloadwire.ExecFailed))
		return errors.Join(ErrSession, err)
	}
	// ExecReached only once the target's exec succeeded; then PID 1 keeps
	// none of the session's descriptors.
	_ = writeAll(controlFD, []byte(payloadwire.ExecReached))
	for _, fd := range []int{controlFD, handoffFD} {
		_ = syscall.Close(fd)
	}
	for fd := 0; fd <= 2; fd++ {
		_ = null(fd)
	}
	go func() {
		for s := range signals {
			// Everyone in the namespace gets it; a terminate is final after a
			// grace period, because PID 1's exit kills the namespace.
			_ = syscall.Kill(-1, s.(syscall.Signal))
			if s == syscall.SIGTERM {
				time.AfterFunc(terminateWait, func() { os.Exit(128 + int(syscall.SIGTERM)) })
			}
		}
	}()
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, 0, nil)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			os.Exit(1)
		}
		if pid == target {
			exit(status)
		}
	}
}

func forward(to func(syscall.Signal)) chan os.Signal {
	signals := make(chan os.Signal, 8)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT)
	if to != nil {
		go func() {
			for s := range signals {
				to(s.(syscall.Signal))
			}
		}()
	}
	return signals
}

func exit(status syscall.WaitStatus) {
	if status.Signaled() {
		os.Exit(128 + int(status.Signal()))
	}
	os.Exit(status.ExitStatus())
}

func null(fd int) error {
	devnull, err := syscall.Open("/dev/null", syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err == nil {
		err = syscall.Dup3(devnull, fd, 0)
		_ = syscall.Close(devnull)
	}
	if err != nil {
		return errors.Join(ErrSession, err)
	}
	return nil
}

func writeAll(fd int, chunks ...[]byte) error {
	for _, raw := range chunks {
		for len(raw) > 0 {
			n, err := syscall.Write(fd, raw)
			if err == syscall.EINTR {
				continue
			}
			if err != nil {
				return err
			}
			raw = raw[n:]
		}
	}
	return nil
}

type fdReader int

func (f fdReader) Read(p []byte) (int, error) {
	for {
		n, err := syscall.Read(int(f), p)
		if err == syscall.EINTR {
			continue
		}
		if n == 0 && err == nil {
			return 0, io.EOF
		}
		if n < 0 {
			n = 0
		}
		return n, err
	}
}
