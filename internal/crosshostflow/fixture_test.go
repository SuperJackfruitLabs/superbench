package crosshostflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func git(t *testing.T, path string, args ...string) string {
	t.Helper()
	c := gitcontrol.Command(context.Background(), path, args...)
	c.Env = append(gitcontrol.Environment(os.Environ()), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

type fixture struct {
	source, dest bench.Store
	spec         workspace.Spec
	ready        *model.Bench
}

func setup(t *testing.T) fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	must(t, err)
	f := fixture{source: bench.Store{State: filepath.Join(root, "source-state"), Data: filepath.Join(root, "source-data")}, dest: bench.Store{State: filepath.Join(root, "dest-state"), Data: filepath.Join(root, "dest-data")}, spec: workspace.Spec{Key: "card_same", CheckpointRepository: "a", QuotaBytes: 1 << 20}}
	for _, name := range []string{"a", "b"} {
		path := filepath.Join(root, "repository-"+name)
		must(t, os.Mkdir(path, 0700))
		git(t, path, "init", "-b", "main")
		must(t, os.WriteFile(filepath.Join(path, "source.txt"), []byte(name+"\n"), 0600))
		must(t, os.WriteFile(filepath.Join(path, ".gitignore"), []byte("private-output\n"), 0600))
		git(t, path, "add", ".")
		git(t, path, "commit", "-m", "fixture")
		f.spec.Repositories = append(f.spec.Repositories, workspace.Repository{Name: name, Repository: path, Branch: "card/same", Base: "main"})
	}
	f.ready, err = f.source.InitializeWorkspaceContext(context.Background(), "source-create", f.spec)
	must(t, err)
	return f
}
func snapshot(t *testing.T, s bench.Store, key string) model.Bench {
	t.Helper()
	rows, err := s.Snapshot(key)
	must(t, err)
	if len(rows) != 1 {
		t.Fatal("missing row")
	}
	return rows[0]
}
