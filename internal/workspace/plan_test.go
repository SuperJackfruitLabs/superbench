package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := gitcontrol.Command(context.Background(), dir, args...)
	cmd.Env = append(gitcontrol.Environment(os.Environ()), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", args[0], err, out)
	}
	return strings.TrimSpace(string(out))
}

func remote(t *testing.T, root, name, quota string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-b", "main")
	if quota != "" {
		if err := os.Mkdir(filepath.Join(dir, ".bench"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".bench", "config.toml"), []byte("[quota]\nsize = \""+quota+"\"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "--allow-empty", "-m", "initial")
	return dir
}

func planner(t *testing.T, root string) Planner {
	t.Helper()
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	return Planner{Mirrors: repository.Mirrors{State: state, Data: filepath.Join(root, "data")}, Git: func(ctx context.Context, dir string, args ...string) (string, error) {
		cmd := gitcontrol.Command(ctx, dir, args...)
		cmd.Env = gitcontrol.Environment(os.Environ())
		out, err := cmd.Output()
		return string(out), err
	}}
}
