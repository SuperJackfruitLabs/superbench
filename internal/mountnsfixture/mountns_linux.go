//go:build linux && freshstoragefixture

// Package mountnsfixture holds the mount-namespace steps of the privileged
// storage fixtures. It builds only into binaries tagged freshstoragefixture and
// is never part of an installed executable.
package mountnsfixture

import (
	"errors"
	"os"
	"os/exec"
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
func Private(fn func() error) error {
	result := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		result <- func() error {
			if err := syscall.Unshare(syscall.CLONE_NEWNS); err != nil {
				return err
			}
			if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
				return err
			}
			if err := syscall.Mount("tmpfs", "/tmp", "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "mode=1777"); err != nil {
				return err
			}
			own, err := os.Stat("/proc/self/ns/mnt")
			if err != nil {
				return err
			}
			private, err := os.Stat("/proc/thread-self/ns/mnt")
			if err != nil || os.SameFile(own, private) {
				return errors.New("private mount namespace unavailable")
			}
			return fn()
		}()
	}()
	return <-result
}

// MountID is findmnt's mount ID for the mount at root, as the calling thread's
// mount namespace sees it.
func MountID(root string) (string, error) {
	out, err := exec.Command("/usr/bin/findmnt", "--noheadings", "--raw", "--output", "ID", "--mountpoint", root).Output()
	if id := strings.TrimSpace(string(out)); err == nil && id != "" && !strings.ContainsAny(id, " \n") {
		return id, nil
	}
	return "", errors.New("mount ID unavailable")
}
