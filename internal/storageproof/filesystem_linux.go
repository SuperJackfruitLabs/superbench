//go:build linux

package storageproof

import (
	"context"
	"syscall"
)

// StateFilesystem is the reboot-stable identity of the filesystem that holds a
// state directory whose st_dev in this boot is device.
func StateFilesystem(ctx context.Context, path string, device uint64) (string, error) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return "", err
	}
	raw, err := VerificationCommand(ctx, "/usr/bin/findmnt", "--noheadings", "--raw", "--output", StateFilesystemColumns, "--target", path)
	if err != nil {
		return "", err
	}
	return ParseStateFilesystem(raw, device, fs.Fsid.X__val)
}
