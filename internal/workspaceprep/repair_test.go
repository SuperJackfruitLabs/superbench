package workspaceprep_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/output"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/retention"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceprep"
)

func repairInspect(t *testing.T, f *fixture, choices ...string) []workspaceprep.RepairMember {
	t.Helper()
	var members []workspaceprep.RepairMember
	if err := f.update(func(rows benchstate.Database, tx *sql.Tx) error {
		var err error
		members, err = f.manager.InspectInitializationRepair(context.Background(), rows["card"], tx, choices)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return members
}

func TestInitializationRepairSalvagesKnownSuccessAtFullRetainedCapacity(t *testing.T) {
	f := newFixture(t, 2, nil)
	f.failProof = true
	if _, err := f.prepare(context.Background(), ""); err == nil {
		t.Fatal("missing proof-save failure")
	}
	if _, err := f.manager.Stop("card", "", true); err != nil {
		t.Fatal(err)
	}
	if err := f.update(func(rows benchstate.Database, tx *sql.Tx) error {
		b := rows["card"]
		for n := 0; n < model.WorkspaceRetainedRuns; n++ {
			id := fmt.Sprintf("%032x", n+1)
			name, _ := retention.Name(id)
			if err := os.WriteFile(filepath.Join(f.store.State, name), []byte("old retained outcome"), 0600); err != nil {
				return err
			}
			entry, err := retention.Snapshot(f.store.State, id)
			if err != nil {
				return err
			}
			if err := history.Start(tx, history.Record{ID: id, Key: b.Key, Holder: "initialization", StartedAt: time.Now().UTC()}); err != nil {
				return err
			}
			zero := 0
			if err := history.Seal(tx, id, entry, 20, &zero, false); err != nil {
				return err
			}
			b.Lease.RetainedRuns = append(b.Lease.RetainedRuns, id)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := f.read()
	members := repairInspect(t, f)
	if members[0].Action != "salvage" || members[0].RunID != before.Lease.Execution.ID || members[1].Action != "prepare" {
		t.Fatal(members)
	}
	rollback := errors.New("unit caller rolls claim back")
	if err := f.update(func(rows benchstate.Database, tx *sql.Tx) error {
		b := rows["card"]
		if err := f.manager.ApplyInitializationRepair(context.Background(), b, tx, members); err != nil {
			return err
		}
		if len(b.Lease.RetainedRuns) != 64 || b.Lease.Execution == nil || len(b.Workspace.Preparation) != 1 || b.Workspace.Preparation[0].RunID != members[0].RunID {
			t.Fatal("salvage appended a65th run or lost known success")
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, f.read()) || f.count(0, "once-count") != 1 || f.count(1, "once-count") != 0 {
		t.Fatal("inspection/apply executed setup or leaked rollback")
	}
}

func TestInitializationRepairPreservesValidOnceAndRestoresGenuineReceipt(t *testing.T) {
	for _, missing := range []string{"proof", "receipt"} {
		t.Run(missing, func(t *testing.T) {
			f := newFixture(t, 2, nil)
			if _, err := f.prepare(context.Background(), "r00"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.manager.Stop("card", "", true); err != nil {
				t.Fatal(err)
			}
			baseline := setupRows(t, f)
			rollback := errors.New("rollback test claim")
			if err := f.update(func(rows benchstate.Database, tx *sql.Tx) error {
				b := rows["card"]
				if missing == "proof" {
					b.Workspace.Preparation = nil
				} else {
					if _, err := tx.Exec("DELETE FROM setup_receipts"); err != nil {
						return err
					}
				}
				members, err := f.manager.InspectInitializationRepair(context.Background(), b, tx, nil)
				if err != nil {
					return err
				}
				want := "preserve"
				if missing == "proof" {
					want = "prepare"
				}
				if members[0].Action != want {
					t.Fatal(members)
				}
				if err := f.manager.ApplyInitializationRepair(context.Background(), b, tx, members); err != nil {
					return err
				}
				var digest, runID, completed string
				if err := tx.QueryRow("SELECT digest,run_id,completed FROM setup_receipts WHERE bench=?", "workspace:"+b.Workspace.ID+":r00").Scan(&digest, &runID, &completed); err != nil {
					return err
				}
				if digest != members[0].Digest || runID == "" || completed == "" {
					t.Fatal("genuine once evidence was lost")
				}
				if missing == "proof" && !strings.Contains(baseline[0], completed) {
					t.Fatal("valid once tuple was needlessly replaced")
				}
				return rollback
			}); !errors.Is(err, rollback) {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(setupRows(t, f), baseline) || f.count(0, "once-count") != 1 {
				t.Fatal("receipt repair repeated once or leaked rollback")
			}
		})
	}
}

func TestInitializationRepairUnknownOnceRequiresReconciliationAndExplicitChoice(t *testing.T) {
	f := newFixture(t, 1, nil)
	physical, err := filepath.EvalSymlinks(f.manager.State)
	if err != nil {
		t.Fatal(err)
	}
	f.manager.State = physical
	f.manager.Seal = func(string, *output.Capture, *int, bool) error { return errors.New("lost history save") }
	if _, err := f.prepare(context.Background(), ""); err == nil {
		t.Fatal("expected unsealed stage")
	}
	if _, err := f.manager.Stop("card", "", true); err != nil {
		t.Fatal(err)
	}
	if err := f.update(func(rows benchstate.Database, tx *sql.Tx) error {
		_, err := f.manager.InspectInitializationRepair(context.Background(), rows["card"], tx, []string{"r00"})
		return err
	}); err == nil {
		t.Fatal("unsealed unknown history accepted")
	}
	plan, err := f.manager.InspectHistory("card")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.manager.ReconcileHistory(*plan); err != nil {
		t.Fatal(err)
	}
	if err := f.update(func(rows benchstate.Database, tx *sql.Tx) error {
		_, err := f.manager.InspectInitializationRepair(context.Background(), rows["card"], tx, nil)
		return err
	}); err == nil {
		t.Fatal("unknown once silently rerunnable")
	}
	members := repairInspect(t, f, "r00")
	if members[0].Action != "rerun-once" || members[0].RunID != "" {
		t.Fatal("unknown became success proof", members)
	}
	before := f.read()
	rollback := errors.New("inspect inherited uncertainty")
	if err := f.update(func(rows benchstate.Database, tx *sql.Tx) error {
		b := workspace.Clone(rows["card"])
		b.Workspace.InitializationRepair = &model.WorkspaceInitializationRepair{ID: "repair-test", Digest: strings.Repeat("d", 64), UncertainOnce: []string{"r00"}, RetiredRuns: []string{b.Lease.Execution.ID}}
		b.Lease.Execution = nil
		b.Workspace.Stage = nil
		if _, err := f.manager.InspectInitializationRepair(context.Background(), b, tx, nil); err == nil {
			t.Fatal("claim then stop forgot uncertain once")
		}
		again, err := f.manager.InspectInitializationRepair(context.Background(), b, tx, []string{"r00"})
		if err != nil {
			return err
		}
		if again[0].Action != "rerun-once" {
			t.Fatal(again)
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, f.read()) || f.count(0, "once-count") != 1 {
		t.Fatal("repair repeated or changed unknown work")
	}
}

func TestInitializationRepairRejectsStaleReceiptAndOrdinaryContinuation(t *testing.T) {
	f := newFixture(t, 1, nil)
	if _, err := f.prepare(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.manager.Stop("card", "", true); err != nil {
		t.Fatal(err)
	}
	members := repairInspect(t, f)
	if err := f.update(func(rows benchstate.Database, tx *sql.Tx) error {
		if _, err := tx.Exec("UPDATE setup_receipts SET completed=?", time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)); err != nil {
			return err
		}
		return f.manager.ApplyInitializationRepair(context.Background(), rows["card"], tx, members)
	}); err == nil {
		t.Fatal("changed raw receipt tuple accepted")
	}
	original := f.manager.Read
	f.manager.Read = func(fn func(benchstate.Database) error) error {
		return original(func(rows benchstate.Database) error {
			b := workspace.Clone(rows["card"])
			b.Lease.StopRequested = false
			b.Lease.Continuation = strings.Repeat("a", 32)
			b.Workspace.InitializationRepair = &model.WorkspaceInitializationRepair{ID: strings.Repeat("b", 32)}
			return fn(benchstate.Database{"card": b})
		})
	}
	before := f.read()
	creates, starts := f.backend.counts()
	for _, id := range []string{"", strings.Repeat("c", 32)} {
		f.manager.RepairID = id
		if _, err := f.prepare(context.Background(), ""); err == nil {
			t.Fatal("ordinary setup bypassed repair identity")
		}
	}
	if c, s := f.backend.counts(); c != creates || s != starts || !reflect.DeepEqual(before, f.read()) {
		t.Fatal("rejected continuation had effects")
	}
}

func TestInitializationRepairKnownFailureStillRequiresOnceChoice(t *testing.T) {
	f := newFixture(t, 1, func(int) string { return "[setup]\nonce='echo once >> once-count'\neach='exit 7'\n" })
	if _, err := f.prepare(context.Background(), ""); err == nil {
		t.Fatal("expected known failed setup")
	}
	if _, err := f.manager.Stop("card", "", true); err != nil {
		t.Fatal(err)
	}
	if err := f.update(func(rows benchstate.Database, tx *sql.Tx) error {
		_, err := f.manager.InspectInitializationRepair(context.Background(), rows["card"], tx, nil)
		return err
	}); err == nil {
		t.Fatal("known failed once attempt silently rerunnable")
	}
	members := repairInspect(t, f, "r00")
	if members[0].Action != "rerun-once" || f.count(0, "once-count") != 1 {
		t.Fatal(members)
	}
}

func TestInitializationRepairRetiredHistoryReleasesOnceBeforeReady(t *testing.T) {
	f := newFixture(t, 1, nil)
	if _, err := f.prepare(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	path := f.read().Workspace.Members[0].Path
	git(t, path, "add", ".")
	git(t, path, "commit", "-m", "preserve preparation output")
	if _, err := f.lifecycle().Checkpoint(context.Background(), "card", f.initial.Lease.Token); err != nil {
		t.Fatal(err)
	}
	before := f.read()
	id := before.Lease.RetainedRuns[0]
	rollback := errors.New("rollback release fixture")
	if err := f.update(func(rows benchstate.Database, tx *sql.Tx) error {
		b := workspace.Clone(rows["card"])
		b.Workspace.Preparation = nil
		b.Lease.RetainedRuns = nil
		b.Workspace.InitializationRepair = &model.WorkspaceInitializationRepair{ID: "repair-test", Digest: strings.Repeat("d", 64), RetiredRuns: []string{id}}
		if err := workspaceprep.ReleaseHistory(tx, b); err != nil {
			return err
		}
		r, err := history.Get(tx, id)
		if err != nil {
			return err
		}
		if r.ReleasedAt.IsZero() {
			t.Fatal("retired initialization history not released")
		}
		b.Workspace.Phase = "ready"
		if err := workspaceprep.ReleaseHistory(tx, b); err != nil {
			t.Fatal("ready operational release re-released retired initialization history", err)
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, f.read()) || !historyRow(t, f, id).ReleasedAt.IsZero() {
		t.Fatal("unit release leaked rollback")
	}
}

func TestInitializationRepairKnownSuccessWithNewInputsIsPreparation(t *testing.T) {
	f := newFixture(t, 1, nil)
	f.failProof = true
	if _, err := f.prepare(context.Background(), ""); err == nil {
		t.Fatal("expected proof save failure")
	}
	if _, err := f.manager.Stop("card", "", true); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.read().Workspace.Members[0].Path, "package.json")
	if err := os.WriteFile(path, []byte("{\"new_input\":true}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	members := repairInspect(t, f)
	if members[0].Action != "prepare" || members[0].RunID != "" {
		t.Fatal("old-input success mislabeled uncertain or salvaged", members)
	}
	if err := f.update(func(rows benchstate.Database, tx *sql.Tx) error {
		_, err := f.manager.InspectInitializationRepair(context.Background(), rows["card"], tx, []string{"r00"})
		return err
	}); err == nil {
		t.Fatal("known success accepted an uncertain-once action")
	}
	if f.count(0, "once-count") != 1 {
		t.Fatal("inspection reran once")
	}
}
