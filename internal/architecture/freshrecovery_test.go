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
// The daemon-uid clean worker and the cleanup model are reached only through
// it, and neither the daemon nor the payload helper can reach any of them.
func TestFreshRecoveryIsReachedOnlyFromTheQuotaCommand(t *testing.T) {
	root := filepath.Join("..", "..")
	module := "forge.superjackfruit.com/SuperJackfruitLabs/superbench/"
	allowed := map[string]map[string]bool{
		module + "internal/freshrecovery":         {"cmd/bench-quota": true, "internal/freshrecovery/fixture": true},
		module + "internal/freshrecovery/fixture": {},
		module + "internal/cleanworker":           {"cmd/bench-quota": true, "internal/freshrecovery": true, "internal/freshrecovery/fixture": true},
		module + "internal/freshcleanup":          {"internal/freshrecovery": true, "internal/freshrecovery/fixture": true},
	}
	// Every import of every non-test file, whatever its build tags: a superset
	// of any one build's graph, so reachability below is conservative.
	graph := map[string][]string{}
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
			if strings.HasPrefix(p, module) {
				graph[rel] = append(graph[rel], strings.TrimPrefix(p, module))
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
	for _, start := range []string{"cmd/benchd", "cmd/bench-payload", "cmd/bench"} {
		seen := map[string]bool{start: true}
		queue := []string{start}
		for len(queue) > 0 {
			next := queue[0]
			queue = queue[1:]
			for _, p := range graph[next] {
				if !seen[p] {
					seen[p] = true
					queue = append(queue, p)
				}
			}
		}
		if len(seen) < 2 {
			t.Errorf("%s import graph not found", start)
		}
		for guarded := range allowed {
			if seen[strings.TrimPrefix(guarded, module)] {
				t.Errorf("%s reaches %s", start, guarded)
			}
		}
	}
}
