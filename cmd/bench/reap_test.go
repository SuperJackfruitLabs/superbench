package main

import (
	"encoding/json"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIReapRequiresUnambiguousConcreteApproval(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("git: %v %s", e, out)
		}
	}
	git(root, "init", "--bare", remote)
	git(root, "init", "-b", "main", seed)
	if e := os.WriteFile(filepath.Join(seed, "file"), []byte("source"), 0600); e != nil {
		t.Fatal(e)
	}
	git(seed, "add", ".")
	git(seed, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "base")
	git(seed, "remote", "add", "origin", remote)
	git(seed, "push", "origin", "main")
	git(remote, "symbolic-ref", "HEAD", "refs/heads/main")
	s := bench.Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data")}
	b, e := s.Create("task", bench.Options{Repo: remote}, false)
	if e != nil {
		t.Fatal(e)
	}
	lease, e := s.Lease("task", "writer")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Checkpoint("task", lease.Lease.Token); e != nil {
		t.Fatal(e)
	}
	if e = s.Release("task", lease.Lease.Token); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ArchiveWithGrace("task", 0); e != nil {
		t.Fatal(e)
	}
	p, e := s.PlanReap("task")
	if e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(p)
	if e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(root, "plan.json")
	if e = os.WriteFile(file, raw, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = approvedReap(s, "task", file, ""); e == nil {
		t.Fatal("no explicit approval required")
	}
	if _, e = approvedReap(s, "task", file, "wrong"); e == nil {
		t.Fatal("wrong approval accepted")
	}
	if e = os.WriteFile(file, []byte(`{"Digest":"reviewed","Digest":"other"}`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = approvedReap(s, "task", file, "other"); e == nil || !strings.Contains(e.Error(), "duplicate plan field") {
		t.Fatal("ambiguous plan was decoded", e)
	}
	if _, e = os.Stat(filepath.Join(b.Path, "file")); e != nil {
		t.Fatal("refusal removed source", e)
	}
	if e = os.WriteFile(file, raw, 0600); e != nil {
		t.Fatal(e)
	}
	result, e := approvedReap(s, "task", file, p.Digest)
	if e != nil || result.ReapedAt == "" {
		t.Fatal("reviewed CLI reap failed", e)
	}
}
