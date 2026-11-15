package localprep_test

import (
	"context"
	"database/sql"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/localprep"
)

func fixture(t *testing.T) (bench.Store, *bench.Bench) {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	git := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, e := c.CombinedOutput(); e != nil {
			t.Fatalf("git: %v %s", e, out)
		}
	}
	git(root, "init", "--bare", remote)
	git(root, "init", "-b", "main", seed)
	if e := os.Mkdir(filepath.Join(seed, ".bench"), 0700); e != nil {
		t.Fatal(e)
	}
	config := "[setup]\nonce = 'echo once >> artifact; echo $npm_config_cache >> cache-paths'\neach = 'echo each >> artifact'\n"
	if e := os.WriteFile(filepath.Join(seed, ".bench", "config.toml"), []byte(config), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(seed, ".gitignore"), []byte("artifact\ncache-paths\n"), 0600); e != nil {
		t.Fatal(e)
	}
	git(seed, "add", ".")
	git(seed, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "fixture")
	git(seed, "remote", "add", "origin", remote)
	git(seed, "push", "origin", "main")
	store := bench.Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data")}
	b, e := store.CreateColdContext(context.Background(), "laptop", bench.Options{Repo: remote}, false)
	if e != nil {
		t.Fatal(e)
	}
	return store, b
}

func TestCooperativePreparationRequiresExplicitStopAndDoesNotCreateSupervisedProof(t *testing.T) {
	s, b := fixture(t)
	owner, e := s.RepairLease("laptop", "first")
	if e != nil {
		t.Fatal(e)
	}
	m := localprep.Manager{State: s.State, Data: s.Data}
	r, e := m.Prepare(context.Background(), "laptop", owner.Lease.Token, false, false)
	if e != nil {
		t.Fatal(e)
	}
	if r.Mode != "cooperative" || r.Acknowledged || r.ExitCode == nil || *r.ExitCode != 0 {
		t.Fatalf("wrong observation: %#v", r)
	}
	if _, e = s.Checkpoint("laptop", owner.Lease.Token); e == nil {
		t.Fatal("checkpoint ignored unresolved cooperative writers")
	}
	if _, e = m.Acknowledge(context.Background(), "laptop", owner.Lease.Token, r.Digest, false); e == nil {
		t.Fatal("missing explicit stop acknowledgement accepted")
	}
	if _, e = m.Acknowledge(context.Background(), "laptop", "wrong", r.Digest, true); e == nil {
		t.Fatal("foreign token accepted")
	}
	if _, e = m.Acknowledge(context.Background(), "laptop", owner.Lease.Token, r.Digest, true); e != nil {
		t.Fatal(e)
	}
	current, e := s.Status("laptop")
	if e != nil {
		t.Fatal(e)
	}
	if current[0].Lease == nil || current[0].Lease.Execution != nil || current[0].Lease.Continuation != "" {
		t.Fatal("cooperative preparation fabricated supervised execution")
	}
	if e = statestore.Read(s.State, func(db *sql.DB) error {
		var n int
		if e := db.QueryRow("SELECT count(*) FROM setup_receipts WHERE bench='laptop'").Scan(&n); e != nil {
			return e
		}
		if n != 0 {
			t.Fatal("cooperative journal fabricated supervised setup receipt")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}

	if _, e = s.Checkpoint("laptop", owner.Lease.Token); e != nil {
		t.Fatal(e)
	}
	if e = s.Release("laptop", owner.Lease.Token); e != nil {
		t.Fatal(e)
	}
	second, e := s.RepairLease("laptop", "second")
	if e != nil {
		t.Fatal(e)
	}
	r2, e := m.Prepare(context.Background(), "laptop", second.Lease.Token, false, false)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Acknowledge(context.Background(), "laptop", second.Lease.Token, r2.Digest, true); e != nil {
		t.Fatal(e)
	}
	bytes, e := os.ReadFile(filepath.Join(b.Path, "artifact"))
	if e != nil || string(bytes) != "once\neach\neach\n" {
		t.Fatalf("warm setup: %q %v", bytes, e)
	}
	cache, e := os.ReadFile(filepath.Join(b.Path, "cache-paths"))
	if e != nil || strings.TrimSpace(string(cache)) != filepath.Join(s.Data, "store", "npm") {
		t.Fatalf("shared cache %q %v", cache, e)
	}
}

func TestCancelledCooperativeOnceRemainsUnknownAndCannotAutomaticallyRerun(t *testing.T) {
	s, b := fixture(t)
	config := "[setup]\nonce = 'echo uncertain >> artifact; sleep 30 & echo $! > child.pid; wait'\n"
	if e := os.WriteFile(filepath.Join(b.Path, ".bench", "config.toml"), []byte(config), 0600); e != nil {
		t.Fatal(e)
	}
	owner, e := s.RepairLease("laptop", "writer")
	if e != nil {
		t.Fatal(e)
	}
	m := localprep.Manager{State: s.State, Data: s.Data}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		record *localprep.Record
		err    error
	}
	done := make(chan result, 1)
	go func() { r, e := m.Prepare(ctx, "laptop", owner.Lease.Token, false, false); done <- result{r, e} }()
	pidPath := filepath.Join(b.Path, "child.pid")
	deadline := time.Now().Add(5 * time.Second)
	var child int
	for time.Now().Before(deadline) {
		raw, e := os.ReadFile(pidPath)
		if e == nil {
			child, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
			if child > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if child <= 0 {
		t.Fatal("hook did not start")
	}
	t.Cleanup(func() {
		p, e := os.FindProcess(child)
		if e == nil {
			_ = p.Kill()
		}
	})
	snapshot, e := m.Status(context.Background(), "laptop", owner.Lease.Token)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Acknowledge(context.Background(), "laptop", owner.Lease.Token, snapshot.Digest, true); e == nil {
		t.Fatal("acknowledged while original leader still active")
	}
	cancel()
	var stopped result
	select {
	case stopped = <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("cooperative cancellation did not return bounded observation")
	}
	if stopped.err == nil || stopped.record == nil || stopped.record.Acknowledged || stopped.record.Successful || !stopped.record.UncertainOnce {
		t.Fatalf("lost unknown owner: %#v %v", stopped.record, stopped.err)
	}
	if _, e = s.Checkpoint("laptop", owner.Lease.Token); e == nil {
		t.Fatal("unknown writer accepted for handoff")
	}
	// The test independently stops its known child before making the operator assertion.
	p, _ := os.FindProcess(child)
	_ = p.Kill()
	if _, e = m.Acknowledge(context.Background(), "laptop", owner.Lease.Token, stopped.record.Digest, true); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Prepare(context.Background(), "laptop", owner.Lease.Token, false, false); e == nil {
		t.Fatal("unknown once hook automatically reran")
	}
	raw, e := os.ReadFile(filepath.Join(b.Path, "artifact"))
	if e != nil || string(raw) != "uncertain\n" {
		t.Fatalf("unexpected repeat %q %v", raw, e)
	}
}

func TestChangedInputsAndCorruptJournalNeverProduceWarmSuccess(t *testing.T) {
	s, b := fixture(t)
	owner, e := s.RepairLease("laptop", "writer")
	if e != nil {
		t.Fatal(e)
	}
	m := localprep.Manager{State: s.State, Data: s.Data}
	r, e := m.Prepare(context.Background(), "laptop", owner.Lease.Token, false, false)
	if e != nil {
		t.Fatal(e)
	}
	configPath := filepath.Join(b.Path, ".bench", "config.toml")
	raw, e := os.ReadFile(configPath)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(configPath, append(raw, []byte("\n# changed after observation\n")...), 0600); e != nil {
		t.Fatal(e)
	}
	ack, e := m.Acknowledge(context.Background(), "laptop", owner.Lease.Token, r.Digest, true)
	if e != nil {
		t.Fatal(e)
	}
	if ack.Successful || !ack.UncertainOnce {
		t.Fatal("changed inputs became preparation success")
	}
	if _, e = m.Prepare(context.Background(), "laptop", owner.Lease.Token, false, false); e == nil {
		t.Fatal("changed unknown once automatically repeated")
	}
	journal := filepath.Join(s.State, "local-preparation-laptop.json")
	if e = os.WriteFile(journal, []byte(`{"Version":"future"}`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Status(context.Background(), "laptop", owner.Lease.Token); e == nil {
		t.Fatal("corrupt journal accepted")
	}
	if _, e = s.Checkpoint("laptop", owner.Lease.Token); e == nil {
		t.Fatal("corrupt journal bypassed checkpoint guard")
	}
	if e = s.Release("laptop", owner.Lease.Token); e == nil {
		t.Fatal("corrupt journal bypassed release guard")
	}
}

func TestCooperativeAcknowledgementDoesNotCacheChangedGitIdentity(t *testing.T) {
	for _, change := range []string{"branch", "origin"} {
		t.Run(change, func(t *testing.T) {
			s, b := fixture(t)
			owner, e := s.RepairLease("laptop", "writer")
			if e != nil {
				t.Fatal(e)
			}
			m := localprep.Manager{State: s.State, Data: s.Data}
			r, e := m.Prepare(context.Background(), "laptop", owner.Lease.Token, false, false)
			if e != nil {
				t.Fatal(e)
			}
			args := []string{"symbolic-ref", "HEAD", "refs/heads/other"}
			if change == "origin" {
				args = []string{"config", "remote.origin.url", filepath.Join(t.TempDir(), "different.git")}
			}
			cmd := exec.Command("git", append([]string{"-C", b.Path}, args...)...)
			if out, e := cmd.CombinedOutput(); e != nil {
				t.Fatalf("identity edit: %v %s", e, out)
			}
			ack, e := m.Acknowledge(context.Background(), "laptop", owner.Lease.Token, r.Digest, true)
			if e != nil {
				t.Fatal("stopped-writer acknowledgement unavailable", e)
			}
			if ack.Successful || ack.PreviousOnceSHA256 == r.InputSHA256 || !ack.UncertainOnce {
				t.Fatal("changed checkout seeded cooperative once success")
			}
		})
	}
}

func TestCooperativeStoppedAcknowledgementPreservesMalformedPartialInputs(t *testing.T) {
	s, b := fixture(t)
	owner, e := s.RepairLease("laptop", "writer")
	if e != nil {
		t.Fatal(e)
	}
	m := localprep.Manager{State: s.State, Data: s.Data}
	r, e := m.Prepare(context.Background(), "laptop", owner.Lease.Token, false, false)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(b.Path, ".bench", "config.toml")
	partial := []byte("[setup\npartial unknown work\n")
	if e = os.WriteFile(path, partial, 0600); e != nil {
		t.Fatal(e)
	}
	ack, e := m.Acknowledge(context.Background(), "laptop", owner.Lease.Token, r.Digest, true)
	if e != nil {
		t.Fatal("explicit stop acknowledgement required overwriting partial work", e)
	}
	if ack.Successful || !ack.UncertainOnce || ack.PreviousOnceSHA256 == r.InputSHA256 {
		t.Fatal("malformed partial inputs fabricated success")
	}
	got, e := os.ReadFile(path)
	if e != nil || string(got) != string(partial) {
		t.Fatal("partial work was modified", e)
	}
}

func TestChangedOnceAttemptInvalidatesEarlierCacheBeforeEffects(t *testing.T) {
	s, b := fixture(t)
	owner, e := s.RepairLease("laptop", "writer")
	if e != nil {
		t.Fatal(e)
	}
	m := localprep.Manager{State: s.State, Data: s.Data}
	original, e := os.ReadFile(filepath.Join(b.Path, ".bench", "config.toml"))
	if e != nil {
		t.Fatal(e)
	}
	first, e := m.Prepare(context.Background(), "laptop", owner.Lease.Token, false, false)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Acknowledge(context.Background(), "laptop", owner.Lease.Token, first.Digest, true); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(b.Path, ".bench", "config.toml")
	if e = os.WriteFile(path, []byte("[setup]\nonce = 'echo changed >> artifact; exit 1'\n"), 0600); e != nil {
		t.Fatal(e)
	}
	failed, e := m.Prepare(context.Background(), "laptop", owner.Lease.Token, false, false)
	if e == nil || failed == nil {
		t.Fatal("changed failing hook did not run")
	}
	if _, e = m.Acknowledge(context.Background(), "laptop", owner.Lease.Token, failed.Digest, true); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, original, 0600); e != nil {
		t.Fatal(e)
	}
	retry, e := m.Prepare(context.Background(), "laptop", owner.Lease.Token, false, true)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Acknowledge(context.Background(), "laptop", owner.Lease.Token, retry.Digest, true); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(filepath.Join(b.Path, "artifact"))
	if e != nil || string(raw) != "once\neach\nchanged\nonce\neach\n" {
		t.Fatalf("old once cache survived intervening effects: %q %v", raw, e)
	}
}

func TestRemovingOnceHookDoesNotForgetUnknownExecution(t *testing.T) {
	s, b := fixture(t)
	owner, e := s.RepairLease("laptop", "writer")
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(b.Path, ".bench", "config.toml")
	original, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, []byte("[setup]\nonce = 'echo partial >> artifact; exit 1'\n"), 0600); e != nil {
		t.Fatal(e)
	}
	m := localprep.Manager{State: s.State, Data: s.Data}
	failed, e := m.Prepare(context.Background(), "laptop", owner.Lease.Token, false, false)
	if e == nil || failed == nil {
		t.Fatal("failing once did not run")
	}
	if _, e = m.Acknowledge(context.Background(), "laptop", owner.Lease.Token, failed.Digest, true); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, []byte("[setup]\n"), 0600); e != nil {
		t.Fatal(e)
	}
	empty, e := m.Prepare(context.Background(), "laptop", owner.Lease.Token, false, true)
	if e != nil {
		t.Fatal(e)
	}
	ack, e := m.Acknowledge(context.Background(), "laptop", owner.Lease.Token, empty.Digest, true)
	if e != nil {
		t.Fatal(e)
	}
	if !ack.UncertainOnce || ack.RanOnce {
		t.Fatal("removing hook erased unknown once evidence")
	}
	if e = os.WriteFile(path, original, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Prepare(context.Background(), "laptop", owner.Lease.Token, false, false); e == nil {
		t.Fatal("restored once silently ran without new explicit approval")
	}
}
