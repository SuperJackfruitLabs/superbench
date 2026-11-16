//go:build linux

package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const Supported = true

type Host struct{}

func bootIdentity() (Identity, error) {
	raw, e := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if e != nil {
		return Identity{}, e
	}
	machine, e := os.ReadFile("/etc/machine-id")
	if e != nil {
		return Identity{}, e
	}
	if strings.TrimSpace(string(machine)) == "" || strings.TrimSpace(string(raw)) == "" {
		return Identity{}, errors.New("stable host and boot identity required")
	}
	digest := sha256.Sum256([]byte("superbench-host:" + strings.TrimSpace(string(machine))))
	return Identity{BootID: strings.TrimSpace(string(raw)), HostID: hex.EncodeToString(digest[:])}, nil
}
func sameBoot(e *Execution) (bool, error) {
	identity, err := bootIdentity()
	if err != nil {
		return false, err
	}
	if identity.HostID != e.HostID {
		return false, errors.New("execution belongs to a different host; fence it on its original host")
	}
	return identity.BootID == e.BootID, nil
}

func scopePath(e *Execution) (string, error) {
	if !filepath.IsAbs(e.Root) || filepath.Clean(e.Root) != e.Root || len(e.ID) != 32 {
		return "", errors.New("invalid execution scope")
	}
	for _, r := range e.ID {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return "", errors.New("invalid execution ID")
		}
	}
	return filepath.Join(e.Root, e.ID), nil
}
func (Host) Create(root, id string) (Identity, error) {
	e := &Execution{Root: root, ID: id}
	path, err := scopePath(e)
	if err != nil {
		return Identity{}, err
	}
	var stat syscall.Statfs_t
	if err = syscall.Statfs(root, &stat); err != nil {
		return Identity{}, fmt.Errorf("delegated cgroup root required: %w", err)
	}
	if stat.Type != 0x63677270 {
		return Identity{}, errors.New("execution root must be a cgroup-v2 filesystem")
	}
	identity, err := bootIdentity()
	if err != nil {
		return Identity{}, err
	}
	if err = os.Mkdir(path, 0700); err != nil {
		return Identity{}, fmt.Errorf("create delegated execution cgroup: %w", err)
	}
	if _, err = os.Stat(filepath.Join(path, "cgroup.kill")); err != nil {
		_ = os.Remove(path)
		return Identity{}, errors.New("cgroup.kill support required")
	}
	return identity, nil
}
func (Host) Start(cmd *exec.Cmd, e *Execution) error {
	path, err := scopePath(e)
	if err != nil {
		return err
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	// clone3 places the child in its scope before executing any user code.
	// Preserve explicit credential drops and other caller process restrictions.
	// Replacing the whole structure would silently run a root caller's child as
	// root even when that caller requested a distinct unprivileged identity.
	attributes := syscall.SysProcAttr{}
	if cmd.SysProcAttr != nil {
		attributes = *cmd.SysProcAttr
	}
	attributes.UseCgroupFD, attributes.CgroupFD = true, fd
	cmd.SysProcAttr = &attributes
	return cmd.Start()
}
func (Host) Empty(e *Execution) error {
	current, err := sameBoot(e)
	if err != nil {
		return err
	}
	if !current {
		return nil
	} // no old process can survive a reboot
	path, err := scopePath(e)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(path, "cgroup.events"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "populated" {
			if fields[1] == "0" {
				return nil
			}
			return errors.New("execution cgroup still populated")
		}
	}
	return errors.New("unable to read execution cgroup population")
}
func (h Host) Fence(e *Execution) error {
	current, err := sameBoot(e)
	if err != nil {
		return err
	}
	if !current {
		return nil
	}
	path, err := scopePath(e)
	if err != nil {
		return err
	}
	if _, err = os.Stat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(path, "cgroup.kill"), []byte("1"), 0600); err != nil {
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err = h.Empty(e); err == nil {
			err = os.Remove(path)
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("fencing deadline exceeded: %w", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
