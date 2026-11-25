package remoteaccess

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
)

// ErrHost is a host that lacks what a remote session needs. Serve refuses it
// before it opens the CA, the host key or the listener, so benchd does not
// start with a door that would refuse every session.
var ErrHost = errors.New("remote access deploy requirement not met")

// host is what Serve checks before it opens anything. Every session mounts a
// private tmpfs over each of shared and moves the bench with mount_setattr,
// so these are what payloadsession.Enter needs, checked once at start.
type host struct {
	varTmp, devShm string
	shared         []string
	// mountSetattr reports whether the kernel has mount_setattr (Linux 5.12).
	mountSetattr func() error
}

// installedHost is the real host.
var installedHost = host{varTmp: "/var/tmp", devShm: "/dev/shm", shared: []string{"/tmp", "/var/tmp", "/dev/shm"}, mountSetattr: probeMountSetattr}

// check refuses, naming the first requirement the host misses.
func (h host) check(store bench.Store) error {
	refuse := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{ErrHost}, args...)...)
	}
	if h.mountSetattr == nil {
		return refuse("no mount_setattr probe")
	}
	if err := h.mountSetattr(); err != nil {
		return refuse("the kernel lacks mount_setattr (Linux 5.12 or later): %v", err)
	}
	if info, err := os.Lstat(h.varTmp); err != nil || info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
		return refuse("%s must be a directory, not a symlink", h.varTmp)
	}
	if info, err := os.Lstat(h.devShm); err != nil || !info.IsDir() {
		return refuse("%s must exist as a directory", h.devShm)
	}
	for _, root := range []struct{ name, path string }{{"data root", store.Data}, {"state root", store.State}} {
		for _, shared := range h.shared {
			// Lexically, as Enter checks the policy's roots: a root at or
			// under shared scratch would vanish under a session's tmpfs.
			rel, err := filepath.Rel(shared, root.path)
			if err != nil || rel != ".." && !strings.HasPrefix(rel, "../") {
				return refuse("the %s %s must not be under %s", root.name, root.path, shared)
			}
		}
	}
	return nil
}
