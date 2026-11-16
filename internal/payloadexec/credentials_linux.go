//go:build linux

package payloadexec

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

var errCredentials = errors.New("managed payload credential drop refused")

// This leaf belongs only in the dedicated fixed helper immediately before exec,
// after policy/caller/scope/layout validation. It must never run in the daemon.
// AllThreadsSyscall refuses unsupported runtimes instead of falling back to a
// single-thread credential or capability change.
func dropCredentials(p Policy) error {
	if os.Getuid() != 0 || os.Geteuid() != 0 {
		return errCredentials
	}
	if err := p.Validate(); err != nil {
		return err
	}
	runtime.LockOSThread()
	// The caller exits on any failure and execs on success; never unlock into a
	// different thread between the final proof and exec.
	prctl := func(option, value uintptr) error {
		_, _, errno := syscall.AllThreadsSyscall6(syscall.SYS_PRCTL, option, value, 0, 0, 0, 0)
		if errno != 0 {
			return errors.Join(errCredentials, errno)
		}
		return nil
	}
	if err := prctl(38, 1); err != nil { // PR_SET_NO_NEW_PRIVS
		return err
	}
	if err := prctl(8, 0); err != nil { // PR_SET_KEEPCAPS
		return err
	}
	if err := prctl(47, 4); err != nil { // PR_CAP_AMBIENT_CLEAR_ALL
		return err
	}
	raw, err := os.ReadFile("/proc/sys/kernel/cap_last_cap")
	if err != nil || len(raw) > 32 {
		return errors.Join(errCredentials, err)
	}
	last, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || last < 0 || last > 63 {
		return errors.Join(errCredentials, err)
	}
	for capability := 0; capability <= last; capability++ {
		if err := prctl(24, uintptr(capability)); err != nil { // PR_CAPBSET_DROP
			return err
		}
	}
	if err := syscall.Setgroups([]int{}); err != nil {
		return errors.Join(errCredentials, err)
	}
	if err := syscall.Setresgid(int(p.PayloadGID), int(p.PayloadGID), int(p.PayloadGID)); err != nil {
		return errors.Join(errCredentials, err)
	}
	if err := syscall.Setresuid(int(p.PayloadUID), int(p.PayloadUID), int(p.PayloadUID)); err != nil {
		return errors.Join(errCredentials, err)
	}
	header := struct{ Version, PID uint32 }{Version: 0x20080522}
	var data [2]struct{ Effective, Permitted, Inheritable uint32 }
	_, _, errno := syscall.AllThreadsSyscall(syscall.SYS_CAPSET, uintptr(unsafe.Pointer(&header)), uintptr(unsafe.Pointer(&data[0])), 0)
	runtime.KeepAlive(header)
	runtime.KeepAlive(data)
	if errno != 0 {
		return errors.Join(errCredentials, errno)
	}
	return verifyDropped(p)
}

func verifyDropped(p Policy) error {
	if uint32(os.Getuid()) != p.PayloadUID || uint32(os.Geteuid()) != p.PayloadUID || uint32(os.Getgid()) != p.PayloadGID || uint32(os.Getegid()) != p.PayloadGID {
		return errCredentials
	}
	groups, err := syscall.Getgroups()
	if err != nil || len(groups) != 0 {
		return errors.Join(errCredentials, err)
	}
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil || len(raw) > 64<<10 {
		return errors.Join(errCredentials, err)
	}
	wanted := map[string]string{"Uid:": fmt.Sprintf("%d %d %d %d", p.PayloadUID, p.PayloadUID, p.PayloadUID, p.PayloadUID), "Gid:": fmt.Sprintf("%d %d %d %d", p.PayloadGID, p.PayloadGID, p.PayloadGID, p.PayloadGID), "Groups:": "", "CapInh:": "0000000000000000", "CapPrm:": "0000000000000000", "CapEff:": "0000000000000000", "CapBnd:": "0000000000000000", "CapAmb:": "0000000000000000", "NoNewPrivs:": "1"}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, _ := strings.Cut(line, "\t")
		want, check := wanted[key]
		if check {
			if seen[key] || strings.Join(strings.Fields(value), " ") != want {
				return errCredentials
			}
			seen[key] = true
		}
	}
	if len(seen) != len(wanted) {
		return errCredentials
	}
	return nil
}
