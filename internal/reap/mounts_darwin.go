//go:build darwin

package reap

import (
	"errors"
	"syscall"
)

func mountPaths() ([]string, error) {
	// MNT_NOWAIT from Darwin's sys/mount.h; syscall.Getfsstat exposes flags but
	// does not export that constant. Avoid waiting on unrelated network mounts.
	const noWait = 2
	for attempt := 0; attempt < 4; attempt++ {
		n, e := syscall.Getfsstat(nil, noWait)
		if e != nil {
			return nil, e
		}
		if n > 100000 {
			return nil, errors.New("mount table exceeds limit")
		}
		mounts := make([]syscall.Statfs_t, n+16)
		n, e = syscall.Getfsstat(mounts, noWait)
		if e != nil {
			return nil, e
		}
		if n >= len(mounts) {
			continue
		}
		paths := make([]string, 0, n)
		for _, mount := range mounts[:n] {
			var path []byte
			for _, value := range mount.Mntonname {
				if value == 0 {
					break
				}
				path = append(path, byte(value))
			}
			paths = append(paths, string(path))
		}
		return paths, nil
	}
	return nil, errors.New("mount table changed repeatedly")
}
