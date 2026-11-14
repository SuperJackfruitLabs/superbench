package setup

import (
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/cachepolicy"
	"os"
	"path/filepath"
)

var ErrNoLockfile = errors.New("no supported frozen lockfile; use bench run with the repository's explicit setup command")

func FrozenCommand(path, data string, offline bool) ([]string, error) {
	config, e := Read(path)
	if e != nil {
		return nil, e
	}
	return FrozenCommandFor(path, cachepolicy.Select(data, path, config.CacheSettings()), offline)
}
func FrozenCommandFor(path string, paths cachepolicy.Paths, offline bool) ([]string, error) {
	candidates := map[string][]string{
		"pnpm-lock.yaml":    {"pnpm", "install", "--frozen-lockfile", "--store-dir", paths.Pnpm, "--package-import-method", "clone"},
		"package-lock.json": {"npm", "ci", "--cache", paths.Npm, "--no-audit", "--no-fund"},
	}
	var selected []string
	for lock, args := range candidates {
		if _, e := os.Stat(filepath.Join(path, lock)); e == nil {
			if selected != nil {
				return nil, errors.New("multiple supported lockfiles; choose an explicit bench run setup command")
			}
			selected = args
		} else if !os.IsNotExist(e) {
			return nil, e
		}
	}
	if selected == nil {
		return nil, ErrNoLockfile
	}
	if offline {
		selected = append(selected, "--offline")
	}
	return selected, nil
}
