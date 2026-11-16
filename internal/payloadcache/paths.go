package payloadcache

import (
	"path/filepath"
	"regexp"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/cachepolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
)

var logical = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)

// Selection is a logical current registration selection, not a physical proof.
// Scalar denotes the explicit fresh scalar layout; Root denotes an aggregate
// root command. The caller must separately match the actual registered layout.
type Selection struct {
	Key, Member  string
	Scalar, Root bool
}

func (s Selection) checkout(data string) (string, error) {
	if !payloadpolicy.Directory(data) || !logical.MatchString(s.Key) || s.Member != "" && !logical.MatchString(s.Member) || s.Scalar && (s.Member != "" || s.Root) || !s.Scalar && s.Root != (s.Member == "") {
		return "", ErrModes
	}
	root := filepath.Join(data, "benches", s.Key)
	if s.Scalar {
		return filepath.Join(root, "worktree"), nil
	}
	if s.Root {
		return root, nil
	}
	return filepath.Join(root, s.Member), nil
}

// Paths derives fixed new payload-store siblings and existing checkout-local
// cache locations. It never creates, inspects, adopts or changes permissions.
// An old daemon store is never selected by this separate-identity path.
func Paths(data string, selection Selection, m Modes) (cachepolicy.Paths, error) {
	checkout, err := selection.checkout(data)
	if err != nil || m.Validate() != nil {
		return cachepolicy.Paths{}, ErrModes
	}
	if selection.Root && (!m.Pnpm || !m.Npm || !m.Bun || !m.Playwright || !m.Go) {
		return cachepolicy.Paths{}, ErrModes
	}
	path := func(name string, shared bool) string {
		if shared {
			return filepath.Join(data, "payload-store", name)
		}
		return filepath.Join(checkout, ".bench", "cache", name)
	}
	return cachepolicy.Paths{Pnpm: path("pnpm", m.Pnpm), Npm: path("npm", m.Npm), Bun: path("bun", m.Bun), Playwright: path("playwright", m.Playwright), GoBuild: path("go-build", m.Go), GoMod: path("go-mod", m.Go)}, nil
}

// Environment yields only cache overrides. The helper merges them with its
// existing bounded environment after verifying physical registration and quota;
// deriving these strings alone must never enable an execution.
func Environment(data string, selection Selection, m Modes) ([]string, error) {
	p, err := Paths(data, selection, m)
	if err != nil {
		return nil, err
	}
	return []string{"npm_config_cache=" + p.Npm, "npm_config_store_dir=" + p.Pnpm, "npm_config_package_import_method=clone", "BUN_INSTALL_CACHE_DIR=" + p.Bun, "PLAYWRIGHT_BROWSERS_PATH=" + p.Playwright, "GOCACHE=" + p.GoBuild, "GOMODCACHE=" + p.GoMod}, nil
}
