package autoarchive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/archivepolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/setup"
)

func git(t *testing.T, path string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, raw string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, path string) string {
	t.Helper()
	git(t, path, "add", ".")
	git(t, path, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "fixture work")
	return git(t, path, "rev-parse", "HEAD")
}

type fixture struct {
	store        bench.Store
	remote, seed string
}

func newFixture(t *testing.T, mode string) fixture {
	t.Helper()
	root := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err == nil && entry.IsDir() {
				return os.Chmod(path, 0700)
			}
			return err
		})
	})
	f := fixture{store: bench.Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data")}, remote: filepath.Join(root, "remote.git"), seed: filepath.Join(root, "seed")}
	git(t, root, "init", "--bare", f.remote)
	git(t, root, "init", "-b", "main", f.seed)
	write(t, filepath.Join(f.seed, "file"), "base")
	if mode != "" {
		if err := os.Mkdir(filepath.Join(f.seed, ".bench"), 0700); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(f.seed, ".bench/config.toml"), "[archive]\nwhen='"+mode+"'\n")
	}
	commit(t, f.seed)
	git(t, f.seed, "remote", "add", "origin", f.remote)
	git(t, f.seed, "push", "origin", "main")
	git(t, f.remote, "symbolic-ref", "HEAD", "refs/heads/main")
	return f
}

func (f fixture) create(t *testing.T, key string) *bench.Bench {
	t.Helper()
	b, err := f.store.CreateColdContext(context.Background(), key, bench.Options{Repo: f.remote}, false)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (f fixture) checkpoint(t *testing.T, b *bench.Bench, change bool) *bench.Bench {
	t.Helper()
	held, err := f.store.RepairLease(b.Key, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if change {
		write(t, filepath.Join(b.Path, "task-work"), b.Key)
		commit(t, b.Path)
	}
	b, err = f.store.Checkpoint(b.Key, held.Lease.Token)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.Release(b.Key, held.Lease.Token); err != nil {
		t.Fatal(err)
	}
	return b
}

func (f fixture) merge(t *testing.T, b *bench.Bench, squash bool) string {
	t.Helper()
	git(t, f.seed, "fetch", "origin", b.Branch)
	if squash {
		git(t, f.seed, "merge", "--squash", "FETCH_HEAD")
		git(t, f.seed, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "squash task into distinct commit")
	} else {
		git(t, f.seed, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "merge", "--no-ff", "FETCH_HEAD", "-m", "merge task")
	}
	git(t, f.seed, "push", "origin", "main")
	return git(t, f.seed, "rev-parse", "HEAD")
}

func state(t *testing.T, f fixture, key string) bench.Bench {
	t.Helper()
	b, err := f.store.Status(key)
	if err != nil || len(b) != 1 {
		t.Fatal(b, err)
	}
	return b[0]
}

func tick(t *testing.T, m *Manager, want string) Result {
	t.Helper()
	r, err := m.Tick(context.Background(), 100)
	if err != nil || len(r.Results) != 1 || r.Results[0].Status != want {
		t.Fatalf("tick = %+v, %v; want %s", r, err, want)
	}
	return r.Results[0]
}

func TestOnlySubsequentMergedCheckpointAutomaticallyArchives(t *testing.T) {
	f := newFixture(t, "branch-merged")
	b := f.create(t, "task")
	base := git(t, f.seed, "rev-parse", "HEAD")
	if b.CreatedFrom != base {
		t.Fatalf("creation base not pinned: %+v", b)
	}
	b = f.checkpoint(t, b, false)
	m := &Manager{Store: f.store}
	tick(t, m, "not eligible")
	if got := state(t, f, b.Key); got.ArchivedAt != "" {
		t.Fatal("fresh branch archived", got)
	}
	b = f.checkpoint(t, b, true)
	if b.Checkpoint == b.CreatedFrom {
		t.Fatal("fixture did not advance checkpoint")
	}
	if b.ArchiveUnmerged == nil || b.ArchiveUnmerged.Checkpoint != b.Checkpoint || b.ArchiveUnmerged.TargetBranch != "refs/heads/main" || b.ArchiveUnmerged.TargetCommit != base {
		t.Fatalf("published task did not retain pre-merge observation: %+v", b.ArchiveUnmerged)
	}
	tick(t, m, "not eligible")
	target := f.merge(t, b, false)
	priorObservation := *b.ArchiveUnmerged
	b = f.checkpoint(t, b, false)
	if b.ArchiveUnmerged == nil || *b.ArchiveUnmerged != priorObservation {
		t.Fatalf("same-checkpoint confirmation after merge erased its original observation: %+v", b.ArchiveUnmerged)
	}
	tick(t, m, "archived")
	a := state(t, f, b.Key)
	at, err := time.Parse(time.RFC3339Nano, a.ArchivedAt)
	if err != nil {
		t.Fatal(err)
	}
	end, err := time.Parse(time.RFC3339Nano, a.ReapAfter)
	if err != nil || end.Sub(at) != archivepolicy.DefaultGrace {
		t.Fatal("wrong archive grace", a.ReapAfter, err)
	}
	c, err := setup.Read(b.Path)
	if err != nil {
		t.Fatal(err)
	}
	if a.ArchiveFrozenAt == "" || a.ArchiveFreezeDigest == "" || a.ReapedAt != "" || a.ArchiveMerge == nil || a.ArchiveMerge.Checkpoint != b.Checkpoint || a.ArchiveMerge.TargetBranch != "refs/heads/main" || a.ArchiveMerge.TargetCommit != target || a.ArchiveMerge.ConfigDigest != c.Digest {
		t.Fatalf("incomplete archive evidence: %+v, %+v", a, a.ArchiveMerge)
	}
	for _, path := range []string{b.Path, filepath.Join(b.Path, "task-work"), filepath.Join(b.Path, ".bench/config.toml")} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm()&0222 != 0 {
			t.Fatal("archive remained writable", path, info, err)
		}
	}
	if _, err := f.store.RepairLease(b.Key, "late-writer"); !errors.Is(err, bench.ErrArchived) {
		t.Fatal("archived lease accepted", err)
	}
	tick(t, m, "archived")
	if got := state(t, f, b.Key); !reflect.DeepEqual(got, a) {
		t.Fatal("repeat sweep changed archive evidence")
	}
}

func TestAutomaticArchiveSkipsHeldDirtyManualAndUnconfirmedWork(t *testing.T) {
	for _, kind := range []string{"held", "dirty", "manual", "absent-policy", "advanced", "deleted", "squash", "hidden-policy-edit", "hidden-policy-enable"} {
		t.Run(kind, func(t *testing.T) {
			mode := "branch-merged"
			if kind == "manual" || kind == "hidden-policy-enable" {
				mode = "manual"
			}
			if kind == "absent-policy" {
				mode = ""
			}
			f := newFixture(t, mode)
			b := f.checkpoint(t, f.create(t, "task"), true)
			f.merge(t, b, kind == "squash")
			want := "not eligible"
			switch kind {
			case "held":
				if _, err := f.store.RepairLease(b.Key, "still-writing"); err != nil {
					t.Fatal(err)
				}
				want = "leased"
			case "dirty":
				write(t, filepath.Join(b.Path, "task-work"), "unsaved")
				want = "blocked"
			case "advanced":
				git(t, f.remote, "update-ref", "refs/heads/"+b.Branch, git(t, f.seed, "rev-parse", "HEAD"))
			case "deleted":
				git(t, f.remote, "update-ref", "-d", "refs/heads/"+b.Branch)
			case "hidden-policy-edit":
				git(t, b.Path, "update-index", "--assume-unchanged", ".bench/config.toml")
				write(t, filepath.Join(b.Path, ".bench/config.toml"), "[archive]\nwhen='manual'\n")
				if got := git(t, b.Path, "status", "--porcelain"); got != "" {
					t.Fatal("fixture edit not hidden", got)
				}
				want = "blocked"
			case "hidden-policy-enable":
				git(t, b.Path, "update-index", "--assume-unchanged", ".bench/config.toml")
				write(t, filepath.Join(b.Path, ".bench/config.toml"), "[archive]\nwhen='branch-merged'\n")
				if got := git(t, b.Path, "status", "--porcelain"); got != "" {
					t.Fatal("fixture edit not hidden", got)
				}
			}
			r := tick(t, &Manager{Store: f.store}, want)
			if kind == "hidden-policy-edit" && !strings.Contains(r.Error, "differs from its checkpoint") {
				t.Fatal(r)
			}
			a := state(t, f, b.Key)
			if a.ArchivedAt != "" || a.ArchiveMerge != nil || a.ArchiveFreezeDigest != "" {
				t.Fatal("ineligible bench gained archive intent", a)
			}
			info, err := os.Stat(b.Path)
			if err != nil || info.Mode().Perm()&0200 == 0 {
				t.Fatal("ineligible checkout frozen", info, err)
			}
			if kind == "held" && a.Lease == nil {
				t.Fatal("sweep released writer")
			}
		})
	}
}

func updateRecord(t *testing.T, f fixture, key string, mutate func(map[string]any), version int) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(f.store.State, "bench.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw string
	if err = db.QueryRow("SELECT record FROM benches WHERE key=?", key).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err = json.Unmarshal([]byte(raw), &record); err != nil {
		t.Fatal(err)
	}
	mutate(record)
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE benches SET record=? WHERE key=?", string(encoded), key); err != nil {
		t.Fatal(err)
	}
	if version == 11 {
		if _, err = db.Exec("UPDATE benches SET record=json_remove(record,'$.RegistrationID'); DROP TABLE IF EXISTS workspace_handoff_imports; DROP TABLE IF EXISTS workspace_handoff_exports; DROP TABLE IF EXISTS workspace_handoff_offers; DROP TABLE IF EXISTS workspace_handoff_identity; DROP TABLE initialization_repair_operations; DROP TABLE workspace_operation_cancellations; DROP TABLE workspace_operation_runs; DROP TABLE workspace_operations; PRAGMA user_version=11"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigratedLegacyBenchDoesNotFabricateCreationBase(t *testing.T) {
	f := newFixture(t, "branch-merged")
	b := f.checkpoint(t, f.create(t, "legacy"), true)
	f.merge(t, b, false)
	updateRecord(t, f, b.Key, func(record map[string]any) {
		delete(record, "CreatedFrom")
		delete(record, "ArchiveUnmerged")
	}, 11)
	if err := f.store.Migrate(); err != nil {
		t.Fatal(err)
	}
	tick(t, &Manager{Store: f.store}, "not eligible")
	if a := state(t, f, b.Key); a.CreatedFrom != "" || a.ArchivedAt != "" {
		t.Fatal("legacy migration invented automatic authority", a)
	}
}

func TestFollowingLaterDefaultCommitIsNotTaskCompletion(t *testing.T) {
	f := newFixture(t, "branch-merged")
	b := f.checkpoint(t, f.create(t, "task"), false)
	write(t, filepath.Join(f.seed, "default-work"), "unrelated default branch work")
	later := commit(t, f.seed)
	git(t, f.seed, "push", "origin", "main")
	git(t, b.Path, "fetch", "origin", "main")
	git(t, b.Path, "merge", "--ff-only", "FETCH_HEAD")
	b = f.checkpoint(t, b, false)
	if b.Checkpoint != later || b.Checkpoint == b.CreatedFrom {
		t.Fatal("fixture did not advance task to newer default", b)
	}
	if b.ArchiveUnmerged != nil {
		t.Fatal("already integrated checkpoint gained an unmerged observation", b.ArchiveUnmerged)
	}
	tick(t, &Manager{Store: f.store}, "not eligible")
	if got := state(t, f, b.Key); got.ArchivedAt != "" || got.ArchiveMerge != nil {
		t.Fatal("following a later default commit was mistaken for task completion", got)
	}
}

func TestManagerResumesCommittedIntentWithoutRecheckingMergeTrigger(t *testing.T) {
	f := newFixture(t, "branch-merged")
	b := f.checkpoint(t, f.create(t, "task"), true)
	f.merge(t, b, false)
	tick(t, &Manager{Store: f.store}, "archived")
	archived := state(t, f, b.Key)
	// Simulate physical freeze completed but its completion save was lost.
	updateRecord(t, f, b.Key, func(record map[string]any) { delete(record, "ArchiveFrozenAt") }, 0)
	// A fresh merge check would now fail. The task checkpoint is still confirmed,
	// which the existing archive completion contract independently requires.
	git(t, f.remote, "symbolic-ref", "HEAD", "refs/heads/unborn")
	tick(t, &Manager{Store: f.store}, "archived")
	resumed := state(t, f, b.Key)
	if resumed.ArchiveFrozenAt == "" || resumed.ArchivedAt != archived.ArchivedAt || resumed.ReapAfter != archived.ReapAfter || resumed.ArchiveFreezeDigest != archived.ArchiveFreezeDigest || !reflect.DeepEqual(resumed.ArchiveMerge, archived.ArchiveMerge) {
		t.Fatal("retry replaced committed archive intent", archived, resumed)
	}
}

func TestManagerRoundRobinBoundsAndCancellation(t *testing.T) {
	f := newFixture(t, "manual")
	for _, key := range []string{"a", "b", "c"} {
		f.create(t, key)
	}
	m := &Manager{Store: f.store}
	for _, want := range []string{"a", "b", "c", "", "a"} {
		r, err := m.Tick(context.Background(), 1)
		if err != nil || r.Next != want || len(r.Results) > 1 {
			t.Fatal(r, err, want)
		}
		if want != "" && (len(r.Results) != 1 || r.Results[0].Key != want) {
			t.Fatal("manual prefix starved later benches", r)
		}
	}
	for _, limit := range []int{-1, 0, 1001} {
		if _, err := m.Tick(context.Background(), limit); err == nil {
			t.Fatal("invalid bound accepted", limit)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := m.Tick(ctx, 100)
	if !errors.Is(err, context.Canceled) || len(r.Results) != 0 {
		t.Fatal("canceled sweep performed work", r, err)
	}
	for _, key := range []string{"a", "b", "c"} {
		if state(t, f, key).ArchivedAt != "" {
			t.Fatal("manual bench archived", key)
		}
	}
}
