//go:build linux && freshstoragefixture

// Package mountnsfixture holds the mount-namespace steps of the privileged
// storage fixtures. It builds only into binaries tagged freshstoragefixture and
// is never part of an installed executable.
package mountnsfixture

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
)

// RequireSupervised refuses unless this process runs in private mount and
// network namespaces, with the original ones (PID 1's) recorded by the
// supervisor in prefix+"MNT" and prefix+"NET", and in the original PID namespace.
func RequireSupervised(prefix string) error {
	for _, kind := range []string{"mnt", "net"} {
		original, err := os.Readlink("/proc/1/ns/" + kind)
		if err != nil || os.Getenv(prefix+strings.ToUpper(kind)) != original {
			return errors.New("original namespace proof missing: " + kind)
		}
		a, err := os.Stat("/proc/1/ns/" + kind)
		if err != nil {
			return err
		}
		b, err := os.Stat("/proc/self/ns/" + kind)
		if err != nil || os.SameFile(a, b) {
			return errors.New("private namespace required: " + kind)
		}
	}
	a, err := os.Stat("/proc/1/ns/pid")
	if err != nil {
		return err
	}
	b, err := os.Stat("/proc/self/ns/pid")
	if err != nil || !os.SameFile(a, b) {
		return errors.New("original PID namespace required")
	}
	return nil
}

// Private runs fn on one OS thread moved into a new mount namespace with
// private propagation and its own /tmp, as systemd builds for a unit with
// PrivateTmp, ProtectHome or ProtectSystem. Every mount there has a new mount ID
// and peer group. Commands fn starts inherit the namespace. The thread stays
// locked when fn returns, so the runtime discards it and the namespace with it.
//
// It never unshares the thread-group leader. /proc/self names the leader, so
// a leader moved into the private namespace would also move every later
// /proc/self reading, and the runtime cannot discard that thread.
func Private(fn func() error) error {
	return onWorkerThread(func() error { return unshared(fn) })
}

// onWorkerThread runs step locked on an OS thread that is not the leader. If
// the first locked thread is the leader, it keeps the leader locked while a
// second goroutine locks another thread, so the runtime cannot hand it the
// leader. step's thread stays locked and is discarded.
func onWorkerThread(step func() error) error {
	result := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		if syscall.Gettid() != syscall.Getpid() {
			result <- step()
			return
		}
		inner := make(chan error, 1)
		go func() {
			runtime.LockOSThread()
			if syscall.Gettid() == syscall.Getpid() {
				inner <- errors.New("no OS thread other than the leader was available")
				return
			}
			inner <- step()
		}()
		err := <-inner
		runtime.UnlockOSThread()
		result <- err
	}()
	return <-result
}

func threadNamespace() (os.FileInfo, error) {
	info, err := os.Stat("/proc/thread-self/ns/mnt")
	if err != nil {
		return nil, fmt.Errorf("read this thread's mount namespace: %w", err)
	}
	return info, nil
}

func unshared(fn func() error) error {
	before, err := threadNamespace()
	if err != nil {
		return err
	}
	if err = syscall.Unshare(syscall.CLONE_NEWNS); err != nil {
		return fmt.Errorf("%w: unshare: %w", ErrNamespace, err)
	}
	after, err := threadNamespace()
	if err != nil {
		return err
	}
	if os.SameFile(before, after) {
		return fmt.Errorf("%w: this thread is still in its original mount namespace after unshare", ErrNamespace)
	}
	if err = syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("%w: make / private: %w", ErrNamespace, err)
	}
	if err = syscall.Mount("tmpfs", "/tmp", "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "mode=1777"); err != nil {
		return fmt.Errorf("%w: private /tmp: %w", ErrNamespace, err)
	}
	return fn()
}

// MountID is the mount ID of the mount at root, read from the calling
// thread's own mountinfo: the namespace this thread is in, with no helper
// process and no /proc/self indirection.
func MountID(root string) (string, error) {
	raw, err := os.ReadFile("/proc/thread-self/mountinfo")
	if err != nil {
		return "", fmt.Errorf("read this thread's mountinfo: %w", err)
	}
	return ParseMountID(raw, root)
}
