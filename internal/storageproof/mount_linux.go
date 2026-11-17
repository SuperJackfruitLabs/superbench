//go:build linux

package storageproof

import (
	"errors"
	"io"
	"os"
	"syscall"
)

// UnmanagedMount names the mount that holds root from this process's bounded
// mount table, for a host without managed storage.
func UnmanagedMount(root string) (string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return "", err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return "", err
	}
	if len(raw) > 1<<20 {
		return "", errors.New("mount table exceeds verification limit")
	}
	return ParseUnmanagedMount(raw, root, uint64(info.Sys().(*syscall.Stat_t).Dev))
}
