// Package freshtree is the daemon's only route into a Fresh workspace's
// payload-private member tree. The daemon never opens those files itself:
// the fixed payload helper observes them, or runs Git or a held stage in them,
// as the payload account. A legacy workspace selects no tree and is unchanged.
package freshtree

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

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

// ErrExportCap refuses a checkpoint whose new objects for one member exceed
// MaxExport. Retrying cannot succeed; the work must be split or made smaller.
var ErrExportCap = errors.New("fresh checkpoint export exceeds the 64 MiB per-member bundle cap")

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

// Resolve applies s, or Select with state when s is nil. A nil Tree is legacy.
func Resolve(s Selector, state string, b *model.Bench) (Tree, error) {
	if s == nil {
		return Select(state, b)
	}
	return s(b)
}

// Select decides from the workspace's own registration in daemon state, never
// from ownership: a registration selects the helper route and fails closed
// when that route is unavailable. Only a workspace with no registration, or a
// caller with no state root, takes the unchanged legacy path.
func Select(state string, b *model.Bench) (Tree, error) {
	registered, err := Registered(state, b)
	if err != nil || !registered {
		return nil, err
	}
	tree, err := installed(b)
	if err != nil || tree == nil {
		return nil, errors.Join(ErrUnavailable, err)
	}
	return tree, nil
}

// MaxRegistration bounds the registration read; it is a payload layout record.
const MaxRegistration = 256 << 10

// Registered reports whether the daemon-owned payload layout registry holds
// b's layout. A registration under b's key for another workspace refuses.
func Registered(state string, b *model.Bench) (bool, error) {
	if state == "" || b == nil || b.Workspace == nil {
		return false, nil
	}
	path := filepath.Join(state, "payload-layouts", b.Key+".json")
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, errors.Join(ErrUnavailable, err)
	}
	if !info.Mode().IsRegular() || info.Size() > MaxRegistration {
		return true, ErrUnavailable
	}
	raw, err := os.ReadFile(path)
	var named struct{ Key, ID string }
	if err != nil || json.Unmarshal(raw, &named) != nil || named.Key != b.Key || named.ID != b.Workspace.ID {
		return true, errors.Join(ErrUnavailable, errors.New("payload layout registration does not name this workspace"), err)
	}
	return true, nil
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
