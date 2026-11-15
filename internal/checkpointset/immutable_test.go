package checkpointset

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func advanceImmutableTask(t *testing.T, f coordinatorFixture, name string) string {
	t.Helper()
	work := f.work[name]
	if err := os.WriteFile(filepath.Join(work, "file"), []byte("destination work"), 0600); err != nil {
		t.Fatal(err)
	}
	cg(t, work, "commit", "-am", "destination advances task")
	cg(t, work, "push", "origin", "HEAD:"+f.m.Members[0].TaskRef)
	return cg(t, work, "rev-parse", "HEAD")
}

func TestVerifyImmutableAllowsAdvancedTaskButPreservesExactSealAndPins(t *testing.T) {
	f := fixtureCoordinator(t)
	ctx := context.Background()
	seal, err := f.c.Complete(ctx, f.m)
	if err != nil {
		t.Fatal(err)
	}
	advanced := advanceImmutableTask(t, f, "alpha")
	if err := confirm(ctx, f.c.Mirrors[f.m.Members[0].Repository], f.m.Members[0].TaskRef, f.m.Members[0].Commit); err == nil {
		t.Fatal("ordinary task confirmation accepted another writer's advanced tip")
	}
	if err := f.c.VerifyImmutable(ctx, f.m, seal.Commit, seal.Digest); err != nil {
		t.Fatal("original immutable evidence became unusable after task advancement", err)
	}
	meta, _ := MetadataRef(f.m.BenchID)
	if got := cg(t, f.roots["alpha"], "rev-parse", meta); got != seal.Commit {
		t.Fatal("verification rewrote metadata seal")
	}
	for _, member := range f.m.Members {
		if got := cg(t, f.roots[member.Name], "rev-parse", member.PinRef); got != member.Commit {
			t.Fatal("verification changed immutable source pin")
		}
		want := member.Commit
		if member.Name == "alpha" {
			want = advanced
		}
		if got := cg(t, f.roots[member.Name], "rev-parse", member.TaskRef); got != want {
			t.Fatal("verification changed a mutable destination task tip")
		}
	}
	// A later valid metadata generation also preserves recovery of this seal.
	next := f.m
	next.Members = append([]Member(nil), f.m.Members...)
	next.GenerationID = strings.Repeat("c", 32)
	next.PreviousManifestCommit = &seal.Commit
	next.Members[0].Commit = advanced
	for i := range next.Members {
		next.Members[i].PinRef, _ = PinRef(next.BenchID, next.GenerationID, next.Members[i].Name)
	}
	cg(t, f.c.Mirrors[next.Members[0].Repository], "fetch", f.work["alpha"], "HEAD")
	if _, err := f.c.Complete(ctx, next); err != nil {
		t.Fatal(err)
	}
	if err := f.c.VerifyImmutable(ctx, f.m, seal.Commit, seal.Digest); err != nil {
		t.Fatal("historical seal on valid metadata history refused", err)
	}
}

func TestVerifyImmutableRejectsChangedRemoteEvidenceAndExpectedVector(t *testing.T) {
	for _, mode := range []string{"missing pin", "changed pin", "missing metadata", "foreign metadata", "wrong seal", "wrong digest", "missing member", "changed commit", "changed task ref", "changed generation", "changed predecessor", "wrong bench", "unknown repository"} {
		t.Run(mode, func(t *testing.T) {
			f := fixtureCoordinator(t)
			ctx := context.Background()
			seal, err := f.c.Complete(ctx, f.m)
			if err != nil {
				t.Fatal(err)
			}
			expected := f.m
			expected.Members = append([]Member(nil), f.m.Members...)
			member := f.m.Members[0]
			meta, _ := MetadataRef(f.m.BenchID)
			base := cg(t, f.work[member.Name], "rev-parse", "HEAD~1")
			switch mode {
			case "missing pin":
				cg(t, f.roots[member.Name], "update-ref", "-d", member.PinRef)
			case "changed pin":
				cg(t, f.roots[member.Name], "update-ref", member.PinRef, base)
			case "missing metadata":
				cg(t, f.roots[member.Name], "update-ref", "-d", meta)
			case "foreign metadata":
				cg(t, f.roots[member.Name], "update-ref", meta, member.Commit)
			case "wrong seal":
				seal.Commit = member.Commit
			case "wrong digest":
				seal.Digest = strings.Repeat("0", 64)
			case "missing member":
				expected.Members = expected.Members[:1]
			case "changed commit":
				expected.Members[0].Commit = base
			case "changed task ref":
				expected.Members[0].TaskRef = "refs/heads/different-task"
			case "changed generation":
				expected.GenerationID = strings.Repeat("d", 32)
				for i := range expected.Members {
					expected.Members[i].PinRef, _ = PinRef(expected.BenchID, expected.GenerationID, expected.Members[i].Name)
				}
			case "changed predecessor":
				expected.PreviousManifestCommit = &base
			case "wrong bench":
				expected.BenchID = strings.Repeat("d", 32)
				for i := range expected.Members {
					expected.Members[i].PinRef, _ = PinRef(expected.BenchID, expected.GenerationID, expected.Members[i].Name)
				}
			case "unknown repository":
				delete(f.c.Mirrors, f.m.Members[1].Repository)
			}
			if err := f.c.VerifyImmutable(ctx, expected, seal.Commit, seal.Digest); err == nil {
				t.Fatal("changed source evidence was accepted")
			}
		})
	}
}

func TestVerifyImmutableCancellation(t *testing.T) {
	f := fixtureCoordinator(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.c.VerifyImmutable(ctx, f.m, strings.Repeat("a", 40), strings.Repeat("b", 64)); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation did not precede remote verification", err)
	}
}
