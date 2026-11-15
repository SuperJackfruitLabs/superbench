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

// The driver is isolated in the SQL adapter; other engine files use the standard
// library. The command may import the engine; reverse imports are prohibited.
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
			module := "forge.superjackfruit.com/SuperJackfruitLabs/superbench/"
			rel, _ := filepath.Rel(root, filepath.Dir(path))
			rel = filepath.ToSlash(rel)
			edges := map[string][]string{
				"internal/bench":             {"internal/runprotection", "internal/workspacerepair", "internal/workspacearchive", "internal/stageexec", "internal/workspaceprep", "internal/workspace", "internal/benchstate", "internal/statestore", "internal/statepath", "internal/gitcontrol", "internal/quotapolicy", "internal/cachepolicy", "internal/archivepolicy", "internal/model", "internal/creation", "internal/repository", "internal/output", "internal/execution", "internal/setup", "internal/hoststorage", "internal/reap", "internal/history", "internal/retention"},
				"internal/workspacerepair":   {"internal/transferguard", "internal/benchstate", "internal/execution", "internal/gitcontrol", "internal/history", "internal/initrepair", "internal/model", "internal/reap", "internal/retention", "internal/statepath", "internal/workspace", "internal/workspaceprep"},
				"internal/workspacearchive":  {"internal/reftransfer", "internal/worktreetransfer", "internal/workspacepolicy", "internal/workspace", "internal/benchstate", "internal/statepath", "internal/checkpointset", "internal/archivepolicy", "internal/model", "internal/reap", "internal/repository", "internal/setup"},
				"internal/workspaceprep":     {"internal/reftransfer", "internal/prepinput", "internal/prepproof", "internal/transferguard", "internal/retention", "internal/reap", "internal/benchstate", "internal/cachepolicy", "internal/execution", "internal/history", "internal/model", "internal/output", "internal/setup", "internal/stageexec", "internal/statepath", "internal/workspace"},
				"internal/workspacecopy":     {"internal/model"},
				"internal/prepinput":         {"internal/setup"},
				"internal/prepproof":         {"internal/model", "internal/history"},
				"internal/transferguard":     {"internal/model", "internal/reftransfer", "internal/worktreetransfer", "internal/hoststorage", "internal/repository"},
				"internal/runprotection":     {"internal/model"},
				"internal/hoststorage":       {"internal/model", "internal/output"},
				"internal/stageexec":         {"internal/execution", "internal/output"},
				"internal/benchstate":        {"internal/reftransfer", "internal/initrepair", "internal/workspacepolicy", "internal/model", "internal/archivepolicy", "internal/history", "internal/quotapolicy", "internal/setup"},
				"internal/gitcontrol":        {"internal/execution"},
				"internal/archivepolicy":     {"internal/gitcontrol", "internal/model", "internal/reap", "internal/repository", "internal/setup"},
				"internal/setup":             {"internal/gitcontrol", "internal/cachepolicy", "internal/quotapolicy"},
				"internal/repository":        {"internal/gitcontrol"},
				"internal/autoarchive":       {"internal/workspacearchive", "internal/bench", "internal/archivepolicy", "internal/model", "internal/repository", "internal/setup"},
				"internal/checkpointset":     {"internal/model", "internal/gitcontrol"},
				"internal/protocolv2":        {"internal/checkpointset", "internal/model"},
				"internal/workspacepolicy":   {"internal/checkpointset", "internal/model", "internal/quotapolicy", "internal/repository"},
				"internal/workspace":         {"internal/workspacecopy", "internal/transferguard", "internal/workspaceplan", "internal/worktreetransfer", "internal/reftransfer", "internal/workspacepolicy", "internal/benchstate", "internal/statepath", "internal/creation", "internal/checkpointset", "internal/model", "internal/quotapolicy", "internal/repository", "internal/setup"},
				"internal/workspaceplan":     {"internal/checkpointset", "internal/model", "internal/quotapolicy", "internal/repository", "internal/setup"},
				"internal/execution":         {"internal/cachepolicy"},
				"internal/model":             {"internal/execution"},
				"internal/creation":          {"internal/model", "internal/repository"},
				"internal/client":            {"internal/protocol", "internal/protocolv2"},
				"internal/history":           {"internal/reap"},
				"internal/retention":         {"internal/history", "internal/reap"},
				"internal/maintenance":       {"internal/retention", "internal/reap"},
				"internal/workspaceops":      {"internal/bench", "internal/execution", "internal/model", "internal/protocolv2", "internal/workspace", "internal/archivepolicy"},
				"internal/workspaceapi":      {"internal/bench", "internal/model", "internal/protocolv2", "internal/workspaceops"},
				"internal/service":           {"internal/protocolv2", "internal/workspaceops", "internal/workspaceapi", "internal/autoarchive", "internal/bench", "internal/protocol", "internal/maintenance"},
				"internal/workspacetransfer": {"internal/history", "internal/reap", "internal/retention", "internal/hoststorage", "internal/bench", "internal/benchstate", "internal/execution", "internal/gitcontrol", "internal/model", "internal/quotapolicy", "internal/reftransfer", "internal/repository", "internal/workspace", "internal/workspacepolicy", "internal/workspacerepair", "internal/worktreetransfer"},
				"cmd/bench":                  {"internal/workspacetransfer", "internal/workspacerepair", "internal/workspaceprep", "internal/protocolv2", "internal/workspaceops", "internal/workspaceapi", "internal/workspacearchive", "internal/autoarchive", "internal/bench", "internal/client", "internal/protocol", "internal/reap", "internal/retention", "internal/history", "internal/maintenance"},
				"cmd/benchd":                 {"internal/bench", "internal/service"},
				"cmd/bench-quota":            {"internal/hoststorage"},
			}
			allowed := !strings.Contains(strings.Split(p, "/")[0], ".")
			for _, target := range edges[rel] {
				if p == module+target {
					allowed = true
				}
			}
			if p == "modernc.org/sqlite" && (rel == "internal/statestore" && filepath.Base(path) == "sqlite.go" || rel == "internal/service" && filepath.Base(path) == "receipts.go") {
				allowed = true
			}
			if p == "github.com/pelletier/go-toml/v2" && rel == "internal/setup" && filepath.Base(path) == "config.go" {
				allowed = true
			}
			if p == "golang.org/x/sys/unix" && rel == "internal/service" && filepath.Base(path) == "peer_darwin.go" {
				allowed = true
			}
			if !allowed {
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
