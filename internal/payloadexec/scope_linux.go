//go:build linux

package payloadexec

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
)

var ErrScope = errors.New("managed payload helper is not inside its exact recorded scope")

// VerifyScope is read-only. A helper must already be inside its recorded cgroup
// before any root helper code executes. Successful verification retains the
// actual cgroup descriptor; a pathname or caller assertion alone is not proof.
func VerifyScope(p Policy, r Request) (*os.File, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	digest, err := policyDigest(p)
	if err != nil || r.PolicyDigest != digest {
		return nil, errors.Join(ErrScope, ErrPolicy)
	}
	root, err := openPhysicalDirectory(p.CgroupRoot, p.DaemonUID)
	if err != nil {
		return nil, errors.Join(ErrScope, err)
	}
	defer root.Close()
	fd, err := syscall.Openat(int(root.Fd()), r.Scope.ID, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.Join(ErrScope, err)
	}
	scope := os.NewFile(uintptr(fd), r.Scope.ID)
	fail := func(err error) (*os.File, error) {
		_ = scope.Close()
		return nil, errors.Join(ErrScope, err)
	}
	info, err := scope.Stat()
	if err != nil {
		return fail(err)
	}
	owner := info.Sys().(*syscall.Stat_t).Uid
	if owner != 0 && owner != p.DaemonUID || info.Mode().Perm()&0022 != 0 {
		return fail(nil)
	}
	var filesystem syscall.Statfs_t
	if err := syscall.Fstatfs(fd, &filesystem); err != nil {
		return fail(err)
	}
	if filesystem.Type != 0x63677270 {
		return fail(nil)
	}
	procsFD, err := syscall.Openat(fd, "cgroup.procs", syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return fail(err)
	}
	procs := os.NewFile(uintptr(procsFD), "cgroup.procs")
	raw, err := io.ReadAll(io.LimitReader(procs, (64<<10)+1))
	_ = procs.Close()
	if err != nil || len(raw) > 64<<10 {
		return fail(err)
	}
	pid := strconv.Itoa(os.Getpid())
	present := false
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == pid {
			present = true
		}
		if _, err := strconv.ParseUint(line, 10, 32); err != nil {
			return fail(err)
		}
	}
	if !present {
		return fail(nil)
	}
	machine, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return fail(err)
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return fail(err)
	}
	hostDigest := sha256.Sum256([]byte("superbench-host:" + strings.TrimSpace(string(machine))))
	if strings.TrimSpace(string(machine)) == "" || hex.EncodeToString(hostDigest[:]) != r.Scope.HostID || strings.TrimSpace(string(boot)) != r.Scope.BootID {
		return fail(nil)
	}
	return scope, nil
}

// recheckScope reopens the recorded scope and requires the very same cgroup.
func recheckScope(p Policy, r Request, scope *os.File) error {
	current, err := VerifyScope(p, r)
	if err != nil {
		return err
	}
	a, aErr := scope.Stat()
	c, cErr := current.Stat()
	_ = current.Close()
	if aErr != nil || cErr != nil || !os.SameFile(a, c) {
		return errors.Join(ErrBoundary, aErr, cErr)
	}
	return nil
}
