package bench

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Standard library is the only runtime dependency of the engine. The command
// may import the engine; the engine cannot import the command or other modules.
func TestArchitecture(t *testing.T) {
	root := filepath.Join("..", "..")
	packageLines := map[string]int{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "bin") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.Count(string(raw), "\n")
		if lines > 1000 {
			t.Errorf("%s exceeds 1000 lines", path)
		}
		packageLines[filepath.Dir(path)] += lines
		if strings.HasSuffix(path, "_test.go") {
			return nil
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
			if strings.Contains(strings.Split(p, "/")[0], ".") && !(filepath.Base(path) == "sqlite.go" && p == "modernc.org/sqlite") && !(strings.HasSuffix(filepath.Dir(path), filepath.Join("cmd", "bench")) && p == "forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench") {
				t.Errorf("%s: forbidden runtime dependency %s", path, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for p, n := range packageLines {
		if n > 4000 {
			t.Errorf("package %s exceeds 4000 lines", p)
		}
	}
}
