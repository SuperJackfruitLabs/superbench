package bench

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func (s Store) Setup(ctx context.Context, key, holder string, offline bool) (*RunResult, error) {
	states, e := s.Status(key)
	if e != nil {
		return nil, e
	}
	args, e := s.setupCommand(states[0].Path, offline)
	if e != nil {
		return nil, e
	}
	return s.Run(ctx, key, holder, args, true)
}
func (s Store) setupCommand(path string, offline bool) ([]string, error) {
	candidates := map[string][]string{
		"pnpm-lock.yaml":    {"pnpm", "install", "--frozen-lockfile", "--store-dir", filepath.Join(s.Data, "store", "pnpm"), "--package-import-method", "clone"},
		"package-lock.json": {"npm", "ci", "--cache", filepath.Join(s.Data, "store", "npm"), "--no-audit", "--no-fund"},
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
		return nil, errors.New("no supported frozen lockfile; use bench run with the repository's explicit setup command")
	}
	if offline {
		selected = append(selected, "--offline")
	}
	return selected, nil
}
func (s Store) cacheEnvironment(env []string) []string {
	overrides := map[string]string{
		"npm_config_cache":         filepath.Join(s.Data, "store", "npm"),
		"BUN_INSTALL_CACHE_DIR":    filepath.Join(s.Data, "store", "bun"),
		"PLAYWRIGHT_BROWSERS_PATH": filepath.Join(s.Data, "store", "playwright"),
		"GOCACHE":                  filepath.Join(s.Data, "store", "go-build"),
		"GOMODCACHE":               filepath.Join(s.Data, "store", "go-mod"),
	}
	clean := commandEnvironment(env)
	var result []string
	for _, v := range clean {
		key, _, _ := strings.Cut(v, "=")
		if _, replace := overrides[key]; !replace {
			result = append(result, v)
		}
	}
	for key, value := range overrides {
		result = append(result, key+"="+value)
	}
	return result
}
