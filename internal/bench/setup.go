package bench

import (
	"context"
	"database/sql"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/setup"
	"path/filepath"
	"strings"
)

func (s Store) Setup(ctx context.Context, key, holder string, offline bool) (*RunResult, error) {
	return s.run(ctx, key, holder, nil, true, func(b *Bench, tx *sql.Tx) ([]string, func(*sql.Tx, string) error, error) {
		c, e := setup.Read(b.Path)
		if e != nil {
			return nil, nil, e
		}
		if c == nil || !c.Hooks() {
			if e = setup.Invalidate(tx, key); e != nil {
				return nil, nil, e
			}
			args, e := s.setupCommand(b.Path, offline)
			return args, nil, e
		}
		if e = setup.Admit(tx, key); e != nil {
			return nil, nil, e
		}
		done, e := setup.Completed(tx, key, c.Digest)
		if e != nil {
			return nil, nil, e
		}
		if !done {
			if e = setup.Invalidate(tx, key); e != nil {
				return nil, nil, e
			}
		}
		finish := func(tx *sql.Tx, runID string) error {
			if e := setup.Verify(b.Path, c.Digest); e != nil {
				return e
			}
			return setup.Record(tx, key, c.Digest, runID)
		}
		return c.Command(done, offline), finish, nil
	})
}
func (s Store) setupCommand(path string, offline bool) ([]string, error) {
	return setup.FrozenCommand(path, s.Data, offline)
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
