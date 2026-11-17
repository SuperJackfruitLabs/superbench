//go:build linux

package freshrecovery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/cleanworker"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshliveness"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
	"golang.org/x/sys/unix"
)

// workerTimeout bounds one worker run, observation or deletion.
const workerTimeout = 20 * time.Minute

const cgroup2Magic = 0x63677270

// runWorker runs the clean worker once: bench-quota re-executes its own running
// image, /proc/self/exe, as "bench-quota clean-worker UID GID". The child gets
// the credentials of the account its mode names (workerAccount: the daemon to
// observe or delete a leak, the payload to purge a reclaimed tree), with no
// supplementary groups, SIGKILL when
// the forking thread dies, its own transient cgroup, an empty environment,
// Dir=/ and only descriptors 0-2. Root writes one bounded frame and reads one
// bounded reply, which it returns only after decoding it as data. Before it
// returns, the cgroup is killed, found empty and removed.
func runWorker(ctx context.Context, p payloadpolicy.Policy, request cleanworker.Request) (cleanworker.Reply, error) {
	frame, err := cleanworker.EncodeRequest(request)
	if err != nil {
		return cleanworker.Reply{}, errors.Join(ErrRecovery, err)
	}
	uid, gid, err := workerAccount(p, request.Mode)
	if err != nil {
		return cleanworker.Reply{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, workerTimeout)
	defer cancel()
	s, err := newScope(p.CgroupRoot)
	if err != nil {
		return cleanworker.Reply{}, err
	}
	type result struct {
		out []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		// The thread is never unlocked: it carries no_new_privs and an empty
		// bounding set into the fork, and Go ends a locked thread when its
		// goroutine exits. It stays alive until the worker is reaped, because
		// Pdeathsig fires when the forking thread, not the process, exits.
		runtime.LockOSThread()
		out, err := spawn(ctx, uid, gid, frame, s, watched(request.Mode))
		done <- result{out, err}
	}()
	res := <-done
	fenceErr := s.fence()
	if res.err != nil || fenceErr != nil {
		return cleanworker.Reply{}, errors.Join(res.err, fenceErr)
	}
	reply, err := cleanworker.DecodeReply(res.out, request.Mode)
	if err != nil {
		return cleanworker.Reply{}, errors.Join(ErrRecovery, err)
	}
	return reply, nil
}

// spawn runs on a locked thread that it restricts first.
func spawn(ctx context.Context, uid, gid uint32, frame []byte, s *scope, watchProgress bool) ([]byte, error) {
	rootKeyring, err := restrictThread()
	if err != nil {
		return nil, err
	}
	// Only stdio reaches the child: anything bench-quota inherited without
	// close-on-exec is marked so here. Go opens its own descriptors that way.
	if err = unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_CLOEXEC); err != nil {
		return nil, fmt.Errorf("close_range: %w", err)
	}
	cmd := exec.CommandContext(ctx, "/proc/self/exe")
	cmd.Args = append([]string{"bench-quota", cleanworker.Command}, cleanworker.Arguments(cleanworker.Identity{UID: uid, GID: gid, Keyring: rootKeyring})...)
	cmd.Env = []string{}
	cmd.Dir = "/"
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	diagnostic := &capped{max: 64 << 10}
	cmd.Stderr = diagnostic
	cmd.SysProcAttr = workerAttributes(uid, gid, s.fd)
	cmd.Cancel = s.kill
	cmd.WaitDelay = 2 * time.Second
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	// A refusal, a kill or a cancelled context closes the worker's stdout, so
	// the exchange cannot outlive the worker. Wait follows every read.
	var watch func(io.Reader) ([]byte, error)
	var ready func(io.Reader, []byte) error
	if watchProgress {
		// A purge runs as the payload account, which can stop it, before or
		// after its ready line; a worker that reports nothing for PurgeIdle is
		// killed rather than left holding operations.lock until workerTimeout.
		watch = func(r io.Reader) ([]byte, error) {
			return freshliveness.WatchedRead(r, freshliveness.PurgeIdle, s.kill, cleanworker.MaxFrame)
		}
		ready = func(r io.Reader, buf []byte) error {
			return freshliveness.WatchedReadFull(r, buf, freshliveness.PurgeIdle, s.kill)
		}
	}
	out, exchangeErr := exchange(stdout, stdin, frame, ready, watch)
	if exchangeErr != nil {
		_ = s.kill()
	}
	waitErr := cmd.Wait()
	if waitErr != nil || exchangeErr != nil {
		return nil, errors.Join(exchangeErr, workerError(errors.Join(waitErr, ErrRecovery), diagnostic.raw))
	}
	if diagnostic.over {
		return nil, ErrRecovery
	}
	return out, nil
}

// exchange sends the request only after the worker has reported, with the
// exact ready line, that every self-check passed: it is non-dumpable and
// untraced, in its own session, with only stdio, exactly the daemon's
// credentials and a keyring that is not root's. Nothing reaches a worker
// that refused or was traced before it became non-dumpable. It then reads
// the bounded reply to EOF; readReady and watch replace the plain reads for a
// watched worker.
func exchange(stdout io.Reader, stdin io.WriteCloser, frame []byte, readReady func(io.Reader, []byte) error, watch func(io.Reader) ([]byte, error)) ([]byte, error) {
	if readReady == nil {
		readReady = func(r io.Reader, buf []byte) error { _, err := io.ReadFull(r, buf); return err }
	}
	ready := make([]byte, len(cleanworker.Ready))
	if err := readReady(stdout, ready); err != nil || string(ready) != cleanworker.Ready {
		stdin.Close()
		return nil, errors.Join(ErrRecovery, err)
	}
	_, err := stdin.Write(frame)
	if closeErr := stdin.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	if watch != nil {
		return watch(stdout)
	}
	out, err := io.ReadAll(io.LimitReader(stdout, cleanworker.MaxFrame+1))
	if err != nil {
		return nil, err
	}
	if len(out) > cleanworker.MaxFrame {
		return nil, ErrRecovery
	}
	return out, nil
}

// workerAttributes are the worker's process attributes: the account's
// credentials with no groups, a session of its own (no controlling tty and out
// of the operator's foreground process group), SIGKILL when the forking thread
// dies, and its own cgroup.
func workerAttributes(uid, gid uint32, cgroup int) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Credential:  &syscall.Credential{Uid: uid, Gid: gid, Groups: []uint32{}},
		Setsid:      true,
		Pdeathsig:   syscall.SIGKILL,
		UseCgroupFD: true,
		CgroupFD:    cgroup,
	}
}

// workerError quotes the worker's diagnostic: it can name daemon-controlled
// paths, and must never reach root's terminal as raw bytes.
func workerError(err error, diagnostic []byte) error {
	return fmt.Errorf("clean worker: %w: %s", err, strconv.Quote(strings.TrimSpace(string(diagnostic))))
}

// restrictThread sets no_new_privs, empties the capability bounding set and
// joins a fresh anonymous session keyring, all on the calling (locked) thread
// only, so the forked worker inherits all three. The thread keeps its
// effective capabilities for clone3 and the credential drop. It returns the
// serial of the session keyring the thread had before, or 0 when that keyring
// was already gone, for the worker to prove it does not hold it.
func restrictThread() (int32, error) {
	if _, _, e := unix.RawSyscall(unix.SYS_PRCTL, unix.PR_SET_NO_NEW_PRIVS, 1, 0); e != 0 {
		return 0, fmt.Errorf("no_new_privs: %w", e)
	}
	raw, err := os.ReadFile("/proc/sys/kernel/cap_last_cap")
	if err != nil || len(raw) > 32 {
		return 0, errors.Join(ErrRecovery, err)
	}
	last, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || last < 0 || last > 63 {
		return 0, errors.Join(ErrRecovery, err)
	}
	for c := 0; c <= last; c++ {
		if _, _, e := unix.RawSyscall(unix.SYS_PRCTL, unix.PR_CAPBSET_DROP, uintptr(c), 0); e != 0 {
			return 0, fmt.Errorf("drop bounding capability %d: %w", c, e)
		}
	}
	return freshKeyring(keyctl)
}

// keyctl is the raw system call on the calling thread.
func keyctl(op, arg uintptr) (int32, error) {
	id, _, e := unix.RawSyscall(unix.SYS_KEYCTL, op, arg, 0)
	if e != 0 {
		return 0, e
	}
	return int32(id), nil
}

// freshKeyring leaves the thread's session keyring for a new anonymous one:
// KEYCTL_JOIN_SESSION_KEYRING with a NULL name always creates a new keyring
// (an empty name would join a shared one). Session keyrings are per thread
// credentials, so only this locked thread, and the worker it forks, change.
// A session keyring that was revoked or expired (an operator's ended login)
// counts as none. The new keyring must be live and differ from the old one.
func freshKeyring(call func(op, arg uintptr) (int32, error)) (int32, error) {
	old, err := call(unix.KEYCTL_GET_KEYRING_ID, uintptr(unix.KEY_SPEC_SESSION_KEYRING&0xffffffff))
	if errors.Is(err, unix.EKEYREVOKED) || errors.Is(err, unix.EKEYEXPIRED) || errors.Is(err, unix.ENOKEY) {
		old, err = 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("session keyring: %w", err)
	}
	fresh, err := call(unix.KEYCTL_JOIN_SESSION_KEYRING, 0)
	if err != nil {
		return 0, fmt.Errorf("join session keyring: %w", err)
	}
	if fresh <= 0 || fresh == old || old < 0 {
		return 0, ErrRecovery
	}
	return old, nil
}

// capped keeps at most max bytes and records an overflow.
type capped struct {
	raw  []byte
	max  int
	over bool
}

func (c *capped) Write(raw []byte) (int, error) {
	if len(c.raw)+len(raw) > c.max {
		c.over = true
		return 0, ErrRecovery
	}
	c.raw = append(c.raw, raw...)
	return len(raw), nil
}

// scope is one worker's transient cgroup.
type scope struct {
	path string
	fd   int
}

// newScope creates a private cgroup for one worker beneath the payload policy's
// cgroup root, the delegated root the bootstrap's Git runner uses. While the
// service is stopped systemd may have removed that delegated cgroup; then the
// scope goes beneath bench-quota's own cgroup instead. Either way the worker
// runs in a cgroup of its own that root kills and removes before it returns.
func newScope(policyRoot string) (*scope, error) {
	root, err := scopeRoot(policyRoot)
	if err != nil {
		return nil, err
	}
	var fs unix.Statfs_t
	if actual, err := filepath.EvalSymlinks(root); err != nil || actual != root {
		return nil, errors.Join(ErrRecovery, err)
	}
	if err = unix.Statfs(root, &fs); err != nil || fs.Type != cgroup2Magic {
		return nil, errors.Join(ErrRecovery, err)
	}
	if _, err = os.Stat(filepath.Join(root, "cgroup.controllers")); err != nil {
		return nil, errors.Join(ErrRecovery, err)
	}
	sweep(root)
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return nil, err
	}
	path := filepath.Join(root, scopePrefix+hex.EncodeToString(random[:]))
	if err = os.Mkdir(path, 0700); err != nil {
		return nil, err
	}
	if _, err = os.Stat(filepath.Join(path, "cgroup.kill")); err != nil {
		_ = os.Remove(path)
		return nil, errors.Join(ErrRecovery, err)
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = os.Remove(path)
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return &scope{path: path, fd: fd}, nil
}

// scopeRoot is the policy's cgroup root when it exists, otherwise the cgroup
// v2 directory this process runs in.
func scopeRoot(policyRoot string) (string, error) {
	info, err := os.Lstat(policyRoot)
	if err == nil {
		if !info.IsDir() {
			return "", ErrRecovery
		}
		return policyRoot, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil || len(raw) > 64<<10 {
		return "", errors.Join(ErrRecovery, err)
	}
	return ownCgroup(raw)
}

// ownCgroup reads the cgroup v2 path from /proc/self/cgroup: the single
// "0::/path" line, canonical and absolute.
func ownCgroup(raw []byte) (string, error) {
	found := ""
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		own, ok := strings.CutPrefix(line, "0::")
		if !ok {
			continue
		}
		if found != "" || !strings.HasPrefix(own, "/") || filepath.Clean(own) != own || strings.Contains(own, " (deleted)") {
			return "", ErrRecovery
		}
		found = filepath.Join("/sys/fs/cgroup", own)
	}
	if found == "" {
		return "", ErrRecovery
	}
	return found, nil
}

const scopePrefix = "superbench-clean-"

// staleScope reports a name this code gives a worker cgroup.
func staleScope(name string) bool {
	id, ok := strings.CutPrefix(name, scopePrefix)
	return ok && len(id) == 32 && isHex(id)
}

// sweep removes worker cgroups an earlier crash left under root, but only
// empty ones: unpopulated and without child cgroups. rmdir refuses anything
// else, and a scope that is populated is never touched. Errors are ignored:
// a leftover scope holds nothing and costs nothing.
func sweep(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !staleScope(entry.Name()) {
			continue
		}
		path := filepath.Join(root, entry.Name())
		events, err := os.ReadFile(filepath.Join(path, "cgroup.events"))
		if err != nil || !freshliveness.Unpopulated(events) {
			continue
		}
		_ = unix.Rmdir(path)
	}
}

func (s *scope) kill() error {
	return os.WriteFile(filepath.Join(s.path, "cgroup.kill"), []byte("1"), 0600)
}

// fence kills anything left in the scope, waits until it is unpopulated and
// removes it. A scope that will not empty is an error: the maintenance gate
// would then refuse the next step on the live daemon-uid process anyway.
func (s *scope) fence() error {
	unix.Close(s.fd)
	if err := s.kill(); err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		raw, err := os.ReadFile(filepath.Join(s.path, "cgroup.events"))
		if err != nil {
			return err
		}
		if freshliveness.Unpopulated(raw) {
			return os.Remove(s.path)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("clean worker cgroup %s still populated", s.path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
