package checkpointset

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func cg(t *testing.T, path string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
	cmd.Env = gitEnvironment()
	out, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("git %s: %v %s", args[0], e, out)
	}
	return strings.TrimSpace(string(out))
}

type coordinatorFixture struct {
	c           Coordinator
	m           Manifest
	roots, work map[string]string
}

func fixtureCoordinator(t *testing.T) coordinatorFixture {
	t.Helper()
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	f := coordinatorFixture{c: Coordinator{State: filepath.Join(root, "state"), Mirrors: map[string]string{}, Gate: func(context.Context, Manifest) error { return nil }}, m: Manifest{Version: Version, BenchID: strings.Repeat("a", 32), GenerationID: strings.Repeat("b", 32), CheckpointRepository: "alpha"}, roots: map[string]string{}, work: map[string]string{}}
	for _, name := range []string{"alpha", "beta"} {
		remote, work, mirror := filepath.Join(root, name+".git"), filepath.Join(root, name+"-work"), filepath.Join(root, name+"-mirror.git")
		cg(t, root, "init", "--bare", remote)
		cg(t, root, "init", "-b", "main", work)
		if e = os.WriteFile(filepath.Join(work, "file"), []byte("initial"), 0600); e != nil {
			t.Fatal(e)
		}
		cg(t, work, "add", "file")
		cg(t, work, "commit", "-m", "initial")
		cg(t, work, "remote", "add", "origin", remote)
		cg(t, work, "push", "origin", "main")
		cg(t, root, "clone", "--mirror", remote, mirror)
		if e = os.WriteFile(filepath.Join(work, "file"), []byte("task"), 0600); e != nil {
			t.Fatal(e)
		}
		cg(t, work, "commit", "-am", "task")
		commit := cg(t, work, "rev-parse", "HEAD")
		cg(t, mirror, "fetch", work, "HEAD")
		pin, _ := PinRef(f.m.BenchID, f.m.GenerationID, name)
		f.m.Members = append(f.m.Members, Member{Name: name, Repository: remote, TaskRef: "refs/heads/card/task", PinRef: pin, ObjectFormat: "sha1", Commit: commit})
		f.c.Mirrors[remote] = mirror
		f.roots[name] = remote
		f.work[name] = work
	}
	return f
}
func rejectRef(t *testing.T, remote, ref string) {
	t.Helper()
	body := "#!/bin/sh\nwhile read old new ref; do\n if [ \"$ref\" = '" + ref + "' ]; then exit 1; fi\ndone\n"
	if e := os.WriteFile(filepath.Join(remote, "hooks", "pre-receive"), []byte(body), 0700); e != nil {
		t.Fatal(e)
	}
}
func TestCoordinatorRetainsPartialPushesAndRetriesSameGeneration(t *testing.T) {
	for _, stage := range []string{"second task", "second pin", "seal"} {
		t.Run(stage, func(t *testing.T) {
			f := fixtureCoordinator(t)
			target := f.m.Members[1].TaskRef
			remote := f.roots["beta"]
			if stage == "second pin" {
				target = f.m.Members[1].PinRef
			}
			if stage == "seal" {
				target, _ = MetadataRef(f.m.BenchID)
				remote = f.roots["alpha"]
			}
			rejectRef(t, remote, target)
			if _, e := f.c.Complete(context.Background(), f.m); e == nil {
				t.Fatal("injected remote failure accepted")
			}
			first := f.m.Members[0]
			if e := confirm(context.Background(), f.c.Mirrors[first.Repository], first.PinRef, first.Commit); e != nil {
				t.Fatal("partial successful pin not preserved", e)
			}
			meta, _ := MetadataRef(f.m.BenchID)
			if tip, e := remoteRef(context.Background(), f.c.Mirrors[first.Repository], meta); e != nil || tip != "" {
				t.Fatal("partial generation sealed", tip, e)
			}
			if e := os.Remove(filepath.Join(remote, "hooks", "pre-receive")); e != nil {
				t.Fatal(e)
			}
			seal, e := f.c.Complete(context.Background(), f.m)
			if e != nil {
				t.Fatal(e)
			}
			replay, e := f.c.Complete(context.Background(), f.m)
			if e != nil || replay != seal {
				t.Fatal("lost reply retry changed seal", replay, seal, e)
			}
			m, e := f.c.Recover(context.Background(), first.Repository, seal.BenchID, seal.Commit, seal.Digest)
			if e != nil || m.GenerationID != f.m.GenerationID {
				t.Fatal(m, e)
			}
			changed := f.m
			changed.Members = append([]Member(nil), f.m.Members...)
			changed.Members[0].Commit = cg(t, f.work["alpha"], "rev-parse", "HEAD~1")
			if _, e = f.c.Complete(context.Background(), changed); e == nil {
				t.Fatal("changed generation accepted")
			}
		})
	}
}
func TestRecoverSealedGenerationAfterNewPartialGenerationAndLostState(t *testing.T) {
	f := fixtureCoordinator(t)
	seal, e := f.c.Complete(context.Background(), f.m)
	if e != nil {
		t.Fatal(e)
	}
	next := f.m
	next.GenerationID = strings.Repeat("c", 32)
	next.Members = append([]Member(nil), f.m.Members...)
	next.PreviousManifestCommit = &seal.Commit
	for i := range next.Members {
		v := &next.Members[i]
		work := f.work[v.Name]
		if e = os.WriteFile(filepath.Join(work, "file"), []byte("next task"), 0600); e != nil {
			t.Fatal(e)
		}
		cg(t, work, "commit", "-am", "next task")
		v.Commit = cg(t, work, "rev-parse", "HEAD")
		cg(t, f.c.Mirrors[v.Repository], "fetch", work, "HEAD")
		v.PinRef, _ = PinRef(next.BenchID, next.GenerationID, v.Name)
	}
	rejectRef(t, f.roots["beta"], next.Members[1].TaskRef)
	if _, e = f.c.Complete(context.Background(), next); e == nil {
		t.Fatal("partial next generation accepted")
	}
	root := t.TempDir()
	fresh := Coordinator{Mirrors: map[string]string{}}
	for name, remote := range f.roots {
		mirror := filepath.Join(root, name+".git")
		cg(t, root, "clone", "--mirror", remote, mirror)
		fresh.Mirrors[remote] = mirror
	}
	recovered, e := fresh.Recover(context.Background(), f.roots["alpha"], seal.BenchID, seal.Commit, seal.Digest)
	if e != nil || recovered.Members[0].Commit != f.m.Members[0].Commit {
		t.Fatal("old seal not recovered", recovered, e)
	}
	if _, e = fresh.Recover(context.Background(), f.roots["alpha"], seal.BenchID, seal.Commit, strings.Repeat("0", 64)); e == nil {
		t.Fatal("wrong digest accepted")
	}
	delete(fresh.Mirrors, f.roots["beta"])
	if _, e = fresh.Recover(context.Background(), f.roots["alpha"], seal.BenchID, seal.Commit, seal.Digest); e == nil {
		t.Fatal("manifest discovered unknown repository")
	}
	fresh.Mirrors[f.roots["beta"]] = f.c.Mirrors[f.roots["beta"]]
	cg(t, f.roots["beta"], "update-ref", "-d", f.m.Members[1].PinRef)
	if _, e = fresh.Recover(context.Background(), f.roots["alpha"], seal.BenchID, seal.Commit, seal.Digest); e == nil {
		t.Fatal("local objects concealed missing remote pin")
	}
}
func TestCoordinatorRequiresGateAndRefusesForeignMetadata(t *testing.T) {
	f := fixtureCoordinator(t)
	f.c.Gate = nil
	if _, e := f.c.Complete(context.Background(), f.m); e == nil {
		t.Fatal("nil gate accepted")
	}
	f.c.Gate = func(context.Context, Manifest) error { return nil }
	meta, _ := MetadataRef(f.m.BenchID)
	cg(t, f.work["alpha"], "push", "origin", "HEAD:"+meta)
	if _, e := f.c.Complete(context.Background(), f.m); e == nil {
		t.Fatal("foreign metadata accepted")
	}
}

func TestCoordinatorRefusesOccupiedPinsAndAlternatePushIdentity(t *testing.T) {
	for _, mode := range []string{"occupied pin", "alternate push URL"} {
		t.Run(mode, func(t *testing.T) {
			f := fixtureCoordinator(t)
			v := f.m.Members[0]
			if mode == "occupied pin" {
				initial := cg(t, f.work[v.Name], "rev-parse", "HEAD~1")
				cg(t, f.roots[v.Name], "update-ref", v.PinRef, initial)
			} else {
				cg(t, f.c.Mirrors[v.Repository], "remote", "set-url", "--push", "origin", f.roots["beta"])
			}
			if _, e := f.c.Complete(context.Background(), f.m); e == nil {
				t.Fatal("unsafe remote state accepted")
			}
		})
	}
}
func TestRecoverRefusesForeignMetadataBranchDespiteLocalSealObjects(t *testing.T) {
	f := fixtureCoordinator(t)
	seal, e := f.c.Complete(context.Background(), f.m)
	if e != nil {
		t.Fatal(e)
	}
	meta, _ := MetadataRef(f.m.BenchID)
	cg(t, f.roots["alpha"], "update-ref", meta, f.m.Members[0].Commit)
	if _, e = f.c.Recover(context.Background(), f.roots["alpha"], seal.BenchID, seal.Commit, seal.Digest); e == nil {
		t.Fatal("foreign metadata branch accepted")
	}
}
func TestCheckpointLockHonorsCancellation(t *testing.T) {
	f := fixtureCoordinator(t)
	unlock, e := f.c.lock(context.Background(), f.m.BenchID)
	if e != nil {
		t.Fatal(e)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if second, e := f.c.lock(ctx, f.m.BenchID); e == nil {
		second()
		t.Fatal("canceled lock wait succeeded")
	}
}

func TestCheckpointHistoryKeepsStableParticipantLayout(t *testing.T) {
	f := fixtureCoordinator(t)
	seal, e := f.c.Complete(context.Background(), f.m)
	if e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"remove member", "rename member", "change task ref", "reuse generation"} {
		t.Run(kind, func(t *testing.T) {
			next := f.m
			next.Members = append([]Member(nil), f.m.Members...)
			next.GenerationID = strings.Repeat("d", 32)
			next.PreviousManifestCommit = &seal.Commit
			switch kind {
			case "remove member":
				next.Members = next.Members[:1]
			case "rename member":
				next.Members[1].Name = "gamma"
			case "change task ref":
				next.Members[1].TaskRef = "refs/heads/card/other"
			case "reuse generation":
				next.GenerationID = f.m.GenerationID
			}
			for i := range next.Members {
				next.Members[i].PinRef, _ = PinRef(next.BenchID, next.GenerationID, next.Members[i].Name)
			}
			retry := f.c
			retry.State = filepath.Join(t.TempDir(), "state")
			retry.State, _ = filepath.Abs(retry.State)
			// A private canonical parent avoids platform /var -> /private/var aliases.
			parent, e := filepath.EvalSymlinks(filepath.Dir(retry.State))
			if e != nil {
				t.Fatal(e)
			}
			retry.State = filepath.Join(parent, "state")
			if _, e = retry.Complete(context.Background(), next); e == nil {
				t.Fatal("sealed layout or generation identity changed")
			}
		})
	}
}
func TestKnownSecondPinConflictPrecedesAllTaskPublication(t *testing.T) {
	f := fixtureCoordinator(t)
	second := f.m.Members[1]
	initial := cg(t, f.work["beta"], "rev-parse", "HEAD~1")
	cg(t, f.roots["beta"], "update-ref", second.PinRef, initial)
	if _, e := f.c.Complete(context.Background(), f.m); e == nil {
		t.Fatal("occupied pin accepted")
	}
	first := f.m.Members[0]
	if tip, e := remoteRef(context.Background(), f.c.Mirrors[first.Repository], first.TaskRef); e != nil || tip != "" {
		t.Fatal("known pin conflict published first task", tip, e)
	}
}
