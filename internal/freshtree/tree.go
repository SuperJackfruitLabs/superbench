// Package freshtree is the daemon's only route into a Fresh workspace's
// payload-private member tree. The daemon never opens those files itself:
// the fixed payload helper observes them, or runs Git or a held stage in them,
// as the payload account. A legacy workspace selects no tree and is unchanged.
package freshtree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/cachepolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/registeredinspection"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/setup"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/stageexec"
)

// ErrUnavailable refuses a daemon operation that would need to read or change
// a payload-private tree and has no payload-side route yet. It touches nothing.
var ErrUnavailable = errors.New("fresh workspace tree is private to the payload account; this operation has no payload-side route")

// ErrCache refuses cache selection the payload helper cannot carry: it has no
// typed cache envelope, and the daemon's shared caches are not payload-writable.
var ErrCache = errors.New("fresh workspace setup has no payload cache transport; remove explicit cache settings and lockfile-driven setup")

// MaxExport bounds one member's exported checkpoint bundle held in memory.
const MaxExport = 64 << 20

// Tree routes every access to one Fresh workspace's members through the
// payload helper. Each call names a registered member of b; none takes a path.
type Tree interface {
	// Root proves the registered physical layout. Clean also refuses any
	// aggregate-root entry outside it.
	Root(ctx context.Context, b *model.Bench, clean bool) error
	// Git runs one fixed read-only observation in a member checkout.
	Git(ctx context.Context, b *model.Bench, member string, args ...string) (string, error)
	// Inputs returns the member's fixed preparation inputs as the helper read them.
	Inputs(ctx context.Context, b *model.Bench, member string) (registeredinspection.Inputs, error)
	// Transport launches b's held starting execution in a member, or in the
	// aggregate root for an empty member, through the payload helper.
	Transport(b *model.Bench, member string) (stageexec.PrepareTransport, error)
	// Export returns a Git bundle of the member's HEAD without the excluded
	// commits, bounded by MaxExport. It requires b's held owner.
	Export(ctx context.Context, b *model.Bench, member string, exclude []string) ([]byte, error)
}

// Selector chooses the tree for a workspace record; nil uses Select.
type Selector func(*model.Bench) (Tree, error)

// Resolve applies s, or Select when s is nil. A nil Tree means legacy access.
func Resolve(s Selector, b *model.Bench) (Tree, error) {
	if s == nil {
		return Select(b)
	}
	return s(b)
}

// Select reads only the root entry's owner, which is visible to the daemon.
// A root the daemon owns, or that is absent, is legacy and keeps its exact
// existing behavior; only the installed payload account's root selects the
// helper route. Off Linux no Fresh tree can exist.
func Select(b *model.Bench) (Tree, error) {
	if b == nil || b.Workspace == nil || b.Path == "" {
		return nil, nil
	}
	info, err := os.Lstat(b.Path)
	if err != nil {
		return nil, nil // the legacy caller reports its own unchanged error
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || int(st.Uid) == os.Geteuid() {
		return nil, nil
	}
	return installed(b, st.Uid)
}

// Admin is the member's registered independent Git directory.
func Admin(b *model.Bench, member string) string {
	return filepath.Join(b.Path, ".superbench-git", member)
}

// Config parses observed inputs exactly as prepinput.Read parses a checkout,
// with the same preparation digest.
func Config(in registeredinspection.Inputs) (*setup.Config, string, error) {
	if err := in.Validate(); err != nil {
		return nil, "", err
	}
	var c *setup.Config
	configDigest := ""
	if in.Config.Present {
		parsed, err := setup.Parse(in.Config.Data)
		if err != nil {
			return nil, "", err
		}
		c, configDigest = parsed, parsed.Digest
	}
	digest, err := in.PreparationDigest(configDigest)
	if err != nil {
		return nil, "", err
	}
	return c, digest, nil
}

// Frozen selects lockfile-driven setup for a Fresh member. The legacy command
// names the daemon's shared caches, so a supported lockfile refuses rather
// than silently dropping them; no lockfile is setup.ErrNoLockfile, as before.
func Frozen(in registeredinspection.Inputs) error {
	for _, f := range in.Manifests {
		if f.Present && (f.Name == "pnpm-lock.yaml" || f.Name == "package-lock.json") {
			return ErrCache
		}
	}
	return setup.ErrNoLockfile
}

// Environment is the daemon-supplied environment of a Fresh stage: none. The
// helper builds the payload's own fixed environment and home. Explicit cache
// settings refuse instead of being discarded. A selection error passes through.
func Environment(c *setup.Config, selection error) ([]string, error) {
	if selection != nil {
		return nil, selection
	}
	if c.CacheSettings() != (cachepolicy.Settings{}) {
		return nil, ErrCache
	}
	return nil, nil
}
