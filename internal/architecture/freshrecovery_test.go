package architecture

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Fresh recovery is direct-root operator code. Only the quota helper command
// may link it; its privileged fixture compiles only under freshstoragefixture.
func TestFreshRecoveryIsReachedOnlyFromTheQuotaCommand(t *testing.T) {
	root := filepath.Join("..", "..")
	module := "forge.superjackfruit.com/SuperJackfruitLabs/superbench/"
	allowed := map[string]map[string]bool{
		module + "internal/freshrecovery":         {"cmd/bench-quota": true, "internal/freshrecovery/fixture": true},
		module + "internal/freshrecovery/fixture": {},
	}
	found := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "bin" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, filepath.Dir(path))
		rel = filepath.ToSlash(rel)
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if rel == "internal/freshrecovery/fixture" {
			found[rel] = true
			if !strings.HasPrefix(string(raw), "//go:build linux && freshstoragefixture\n") {
				t.Errorf("%s: the recovery fixture must build only with freshstoragefixture", path)
			}
		}
		tree, err := parser.ParseFile(token.NewFileSet(), path, raw, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range tree.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if importers, guarded := allowed[p]; guarded && !importers[rel] {
				t.Errorf("%s: %s may not import %s", path, rel, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !found["internal/freshrecovery/fixture"] {
		t.Error("recovery fixture package not found")
	}
}
