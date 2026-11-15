package checkpointset_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/checkpointset"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
)

func TestImmutableSourceVerificationDoesNotRelaxOrdinaryLifecycleConfirm(t *testing.T) {
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(ctx context.Context, dir string, args ...string) (string, error) {
		cmd := gitcontrol.Command(ctx, dir, args...)
		cmd.Env = append(gitcontrol.Environment(os.Environ()), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
		out, err := cmd.Output()
		return string(out), err
	}
	run := func(dir string, args ...string) string {
		t.Helper()
		out, err := git(ctx, dir, args...)
		if err != nil {
			t.Fatal("fixture Git failed", args[0], err)
		}
		return strings.TrimSpace(out)
	}
	remote, work := filepath.Join(root, "remote.git"), filepath.Join(root, "work")
	run(root, "init", "--bare", remote)
	run(root, "init", "-b", "main", work)
	if err := os.WriteFile(filepath.Join(work, "file"), []byte("original source"), 0600); err != nil {
		t.Fatal(err)
	}
	run(work, "add", "file")
	run(work, "commit", "-m", "source")
	run(work, "remote", "add", "origin", remote)
	run(work, "push", "origin", "main")
	store := bench.Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data")}
	b, err := store.InitializeWorkspaceContext(ctx, "immutable-source", workspace.Spec{Key: "source", CheckpointRepository: "one", Repositories: []workspace.Repository{{Name: "one", Repository: remote}}})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := workspace.Lifecycle{State: store.State, Data: store.Data, Git: git}
	if err := lifecycle.Confirm(ctx, b); err != nil {
		t.Fatal("baseline lifecycle confirmation failed", err)
	}
	expected, err := workspacepolicy.Manifest(b, b.Workspace.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "file"), []byte("new destination"), 0600); err != nil {
		t.Fatal(err)
	}
	run(work, "commit", "-am", "destination")
	advanced := run(work, "rev-parse", "HEAD")
	run(work, "push", "origin", "HEAD:"+expected.Members[0].TaskRef)
	coordinator := checkpointset.Coordinator{Mirrors: map[string]string{remote: b.Workspace.Members[0].Mirror}}
	cp := b.Workspace.Checkpoint
	if err := coordinator.VerifyImmutable(ctx, expected, cp.Seal, cp.Digest); err != nil {
		t.Fatal("immutable source confirmation refused task advancement", err)
	}
	if err := lifecycle.Confirm(ctx, b); err == nil || !strings.Contains(err.Error(), "task branch differs") {
		t.Fatal("ordinary lifecycle stopped enforcing the current task vector", err)
	}
	member := b.Workspace.Members[0]
	if run(member.Path, "rev-parse", "HEAD") != expected.Members[0].Commit {
		t.Fatal("source checkout HEAD changed during verification")
	}
	if raw, err := os.ReadFile(filepath.Join(member.Path, "file")); err != nil || string(raw) != "original source" {
		t.Fatal("source files changed during verification", err)
	}
	if run(remote, "rev-parse", expected.Members[0].TaskRef) != advanced {
		t.Fatal("immutable verification rewound the destination task ref")
	}
}
