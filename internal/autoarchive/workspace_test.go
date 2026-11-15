package autoarchive

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

func aggregate(t *testing.T, secondMode string) ([2]fixture, *bench.Bench) {
	t.Helper()
	f := [2]fixture{newFixture(t, "branch-merged"), newFixture(t, secondMode)}
	b, err := f[0].store.InitializeWorkspaceContext(context.Background(), "create-vector", workspace.Spec{Key: "vector", CheckpointRepository: "a", Repositories: []workspace.Repository{
		{Name: "a", Repository: f[0].remote}, {Name: "b", Repository: f[1].remote},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return f, b
}

func aggregateWork(t *testing.T, f [2]fixture, b *bench.Bench, names ...string) *bench.Bench {
	t.Helper()
	ctx := context.Background()
	held, err := f[0].store.AcquireWorkspaceContext(ctx, b.Key, "test-owner")
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range held.Workspace.Members {
		for _, name := range names {
			if name == member.Name {
				write(t, filepath.Join(member.Path, "task-work"), member.Name)
				commit(t, member.Path)
			}
		}
	}
	b, err = f[0].store.CheckpointWorkspaceContext(ctx, b.Key, held.Lease.Token)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Workspace.Unmerged) != len(names) {
		t.Fatalf("missing prior unmerged observations: %+v", b.Workspace.Unmerged)
	}
	b, err = f[0].store.ReleaseWorkspaceContext(ctx, b.Key, held.Lease.Token)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestWorkspaceAutomaticArchiveRequiresChangedCheckpointThenCompleteMerge(t *testing.T) {
	f, b := aggregate(t, "branch-merged")
	m := &Manager{Store: f[0].store}
	tick(t, m, "not eligible") // An integrated genesis is not completed work.
	b = aggregateWork(t, f, b, "a", "b")
	tick(t, m, "not eligible")
	f[0].merge(t, &bench.Bench{Branch: b.Workspace.Members[0].Branch}, false)
	tick(t, m, "not eligible") // One integrated member cannot archive the vector.
	f[1].merge(t, &bench.Bench{Branch: b.Workspace.Members[1].Branch}, false)
	tick(t, m, "archived")
	archived := aggregateState(t, f[0], b.Key)
	if archived.Workspace.Phase != "archived" || archived.Lease != nil || archived.Workspace.Archive.FrozenAt == "" || len(archived.Workspace.Archive.Merge) != 2 || archived.ArchivedAt != "" {
		t.Fatalf("archive did not retain whole-vector authority: %+v", archived)
	}
	for i, proof := range archived.Workspace.Archive.Merge {
		if proof.Name != b.Workspace.Members[i].Name || proof.Evidence.Checkpoint != b.Workspace.Checkpoint.Members[i].Commit {
			t.Fatal("archive evidence differs from sealed vector")
		}
	}
	tick(t, m, "archived")
}

func TestWorkspaceAutomaticArchiveAllowsIntegratedUnchangedBaseline(t *testing.T) {
	f, b := aggregate(t, "branch-merged")
	b = aggregateWork(t, f, b, "a")
	f[0].merge(t, &bench.Bench{Branch: b.Workspace.Members[0].Branch}, false)
	tick(t, &Manager{Store: f[0].store}, "archived")
	if b.Workspace.Checkpoint.Members[1].Commit != b.Workspace.Members[1].Source {
		t.Fatal("fixture changed baseline member")
	}
}

func TestWorkspaceAutomaticArchiveRefusesWorkFirstObservedAfterMerge(t *testing.T) {
	f, b := aggregate(t, "branch-merged")
	ctx := context.Background()
	held, err := f[0].store.AcquireWorkspaceContext(ctx, b.Key, "test-owner")
	if err != nil {
		t.Fatal(err)
	}
	member := held.Workspace.Members[0]
	write(t, filepath.Join(member.Path, "task-work"), "already integrated")
	commit(t, member.Path)
	git(t, member.Path, "push", "origin", member.Branch)
	f[0].merge(t, &bench.Bench{Branch: member.Branch}, false)
	b, err = f[0].store.CheckpointWorkspaceContext(ctx, b.Key, held.Lease.Token)
	if err != nil || len(b.Workspace.Unmerged) != 0 {
		t.Fatal("fabricated an unmerged observation", err)
	}
	if _, err = f[0].store.ReleaseWorkspaceContext(ctx, b.Key, held.Lease.Token); err != nil {
		t.Fatal(err)
	}
	tick(t, &Manager{Store: f[0].store}, "not eligible")
}

func TestWorkspaceAutomaticArchiveRequiresEveryPolicyAndCurrentTaskRef(t *testing.T) {
	for _, scenario := range []string{"manual-policy", "moved-ref", "deleted-ref", "changed-target", "hidden-policy"} {
		t.Run(scenario, func(t *testing.T) {
			mode := "branch-merged"
			if scenario == "manual-policy" {
				mode = "manual"
			}
			f, b := aggregate(t, mode)
			b = aggregateWork(t, f, b, "a")
			f[0].merge(t, &bench.Bench{Branch: b.Workspace.Members[0].Branch}, false)
			member := b.Workspace.Members[0]
			want := "not eligible"
			switch scenario {
			case "moved-ref":
				git(t, f[0].remote, "update-ref", "refs/heads/"+member.Branch, member.Source)
				want = "blocked"
			case "deleted-ref":
				git(t, f[0].remote, "update-ref", "-d", "refs/heads/"+member.Branch)
				want = "blocked"
			case "changed-target":
				git(t, f[0].remote, "branch", "other", "main")
				git(t, f[0].remote, "symbolic-ref", "HEAD", "refs/heads/other")
			case "hidden-policy":
				git(t, member.Path, "update-index", "--assume-unchanged", ".bench/config.toml")
				if err := os.WriteFile(filepath.Join(member.Path, ".bench/config.toml"), []byte("[archive]\nwhen='manual'\n"), 0600); err != nil {
					t.Fatal(err)
				}
				want = "blocked"
			}
			tick(t, &Manager{Store: f[0].store}, want)
			current := aggregateState(t, f[0], b.Key)
			if current.Workspace.Archive != nil || current.Workspace.Phase != "ready" {
				t.Fatal("ineligible vector committed an archive intent")
			}
		})
	}
}

func aggregateState(t *testing.T, f fixture, key string) bench.Bench {
	t.Helper()
	rows, err := f.store.Snapshot(key)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	return rows[0]
}
