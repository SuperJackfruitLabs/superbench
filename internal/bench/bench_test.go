package bench

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func g(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, e := git(dir, args...)
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func fixture(t *testing.T) (Store, string) {
	t.Helper()
	r := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.WalkDir(r, func(path string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.IsDir() {
				return os.Chmod(path, 0700)
			}
			return nil
		})
	})
	remote := filepath.Join(r, "remote.git")
	seed := filepath.Join(r, "seed")
	g(t, r, "init", "--bare", remote)
	g(t, r, "init", "-b", "main", seed)
	g(t, seed, "config", "user.email", "test@example.invalid")
	g(t, seed, "config", "user.name", "Test")
	write(t, filepath.Join(seed, "file"), "base")
	g(t, seed, "add", ".")
	g(t, seed, "commit", "-m", "base")
	g(t, seed, "remote", "add", "origin", remote)
	g(t, seed, "push", "origin", "main")
	g(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	return Store{State: filepath.Join(r, "state"), Data: filepath.Join(r, "data")}, remote
}
func write(t *testing.T, p, s string) {
	t.Helper()
	if e := os.WriteFile(p, []byte(s), 0600); e != nil {
		t.Fatal(e)
	}
}
func commit(t *testing.T, b *Bench) string {
	t.Helper()
	write(t, filepath.Join(b.Path, "file"), "failing test")
	g(t, b.Path, "add", ".")
	g(t, b.Path, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "test")
	return g(t, b.Path, "rev-parse", "HEAD")
}
func TestHandoffRecovery(t *testing.T) {
	s, r := fixture(t)
	b, e := s.Create("task", Options{Repo: r}, false)
	if e != nil {
		t.Fatal(e)
	}
	l, e := s.Lease("task", "first")
	if e != nil {
		t.Fatal(e)
	}
	token := l.Lease.Token
	if e = s.Release("task", token); e == nil {
		t.Fatal("released without checkpoint")
	}
	write(t, filepath.Join(b.Path, "untracked"), "keep")
	if _, e = s.Checkpoint("task", token); e == nil {
		t.Fatal("accepted dirty checkpoint")
	}
	if e = os.Remove(filepath.Join(b.Path, "untracked")); e != nil {
		t.Fatal(e)
	}
	sha := commit(t, b)
	if _, e = s.Checkpoint("task", "wrong"); e == nil {
		t.Fatal("accepted wrong token")
	}
	cp, e := s.Checkpoint("task", token)
	if e != nil {
		t.Fatal(e)
	}
	if cp.Lease.Token != "" {
		t.Fatal("token leaked")
	}
	if e = s.Release("task", token); e != nil {
		t.Fatal(e)
	}
	second, e := s.Lease("task", "second")
	if e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(filepath.Join(b.Path, "file"))
	if e != nil || string(raw) != "failing test" {
		t.Fatal("handoff lost files", e)
	}
	if e = s.Release("task", token); e == nil {
		t.Fatal("old token worked")
	}
	root := t.TempDir()
	other := Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data")}
	recovered, e := other.Create("task", Options{Repo: r, Branch: b.Branch}, true)
	if e != nil {
		t.Fatal(e)
	}
	if g(t, recovered.Path, "rev-parse", "HEAD") != sha {
		t.Fatal("wrong recovery")
	}
	if e = s.Release("task", second.Lease.Token); e != nil {
		t.Fatal(e)
	}
}
func TestConcurrentLeaseRestart(t *testing.T) {
	s, r := fixture(t)
	if _, e := s.Create("task", Options{Repo: r}, false); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for range 12 {
		wg.Go(func() { _, e := s.Lease("task", "worker"); results <- e })
	}
	wg.Wait()
	close(results)
	wins := 0
	for e := range results {
		if e == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("winners %d", wins)
	}
	restarted := Store{State: s.State, Data: s.Data}
	if _, e := restarted.Lease("task", "other"); e == nil {
		t.Fatal("restart cleared lease")
	}
	states, e := s.Status("task")
	if e != nil {
		t.Fatal(e)
	}
	if states[0].Lease.Token != "" {
		t.Fatal("status leaked token")
	}
}
func TestFailedPushRetainsLease(t *testing.T) {
	s, r := fixture(t)
	b, e := s.Create("task", Options{Repo: r}, false)
	if e != nil {
		t.Fatal(e)
	}
	l, e := s.Lease("task", "worker")
	if e != nil {
		t.Fatal(e)
	}
	commit(t, b)
	if e = os.WriteFile(filepath.Join(r, "hooks", "pre-receive"), []byte("#!/bin/sh\nexit 1\n"), 0700); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Checkpoint("task", l.Lease.Token); e == nil {
		t.Fatal("rejected push accepted")
	}
	if e = s.Release("task", l.Lease.Token); e == nil {
		t.Fatal("released failed checkpoint")
	}
	if _, e = s.Lease("task", "other"); e == nil {
		t.Fatal("lost lease")
	}
}
func TestValidation(t *testing.T) {
	s, r := fixture(t)
	for _, key := range []string{"../escape", "-bad", "a/b"} {
		if _, e := s.Create(key, Options{Repo: r}, false); e == nil {
			t.Fatal("bad key accepted")
		}
	}
	if _, e := s.Create("missing", Options{Repo: r}, true); e == nil {
		t.Fatal("missing branch recovered")
	}
	bad := Store{State: filepath.Join(s.Data, "state"), Data: s.Data}
	if _, e := bad.Status(""); e == nil {
		t.Fatal("nested state accepted")
	}
	if e := os.MkdirAll(s.Data, 0700); e != nil {
		t.Fatal(e)
	}
	alias := filepath.Join(filepath.Dir(s.Data), "alias")
	if e := os.Symlink(s.Data, alias); e != nil {
		t.Fatal(e)
	}
	bad.State = filepath.Join(alias, "state")
	if _, e := bad.Status(""); e == nil {
		t.Fatal("symlink nesting accepted")
	}
}
