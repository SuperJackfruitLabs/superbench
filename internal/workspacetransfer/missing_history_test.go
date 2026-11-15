package workspacetransfer

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/retention"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
)

func historyTransferFixture(t *testing.T) (string, *sql.Tx, *model.Bench) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := statestore.Open(filepath.Join(root, "test.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	if err = history.Schema(tx); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 32)
	if err = history.Start(tx, history.Record{ID: id, Key: "source", Holder: "owner", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	b := &model.Bench{Key: "source", Workspace: &model.Workspace{}, Lease: &model.Lease{Holder: "owner", StopRequested: true, Execution: &model.Execution{ID: id, Phase: "stopped", Log: filepath.Join(root, "run-"+id+".log"), LogLimitBytes: 1024}}}
	return root, tx, b
}

func TestMissingTransferHistoryNeedsExplicitUnknownReview(t *testing.T) {
	root, tx, b := historyTransferFixture(t)
	var locks transferCaptures
	defer locks.close()
	if _, err := snapshotTransferHistory(context.Background(), root, b, tx, &locks); err == nil {
		t.Fatal("unsealed execution was silently treated as a completed outcome")
	}
	id := b.Lease.Execution.ID
	prior, err := history.Get(tx, id)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := history.Digest(prior)
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := history.ObserveInterrupted(tx, id, digest, history.InterruptedObservation{ApprovalDigest: strings.Repeat("b", 64), PriorDigest: digest, LogState: "absent", ObservedAt: time.Now().UTC()}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := snapshotTransferHistory(context.Background(), root, b, tx, &locks)
	if err != nil || len(observed) != 1 || observed[0].File != nil {
		t.Fatal("reviewed absent capture was not preserved", err)
	}
	after, err := history.Get(tx, id)
	if err != nil || !reflect.DeepEqual(unknown, after) || after.ExitCode != nil || !after.ReleasedAt.IsZero() || after.Checkpoint != "" {
		t.Fatal("inspection changed unknown outcome or associated it with a checkpoint", err)
	}
	if err = os.WriteFile(b.Lease.Execution.Log, []byte("late evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = locks.check(root); err == nil {
		t.Fatal("late capture did not invalidate the concrete absence approval")
	}
}

func TestMissingTransferHistoryPreservesSealedCaptureAndRejectsChange(t *testing.T) {
	root, tx, b := historyTransferFixture(t)
	if err := os.WriteFile(b.Lease.Execution.Log, []byte("retained run output"), 0600); err != nil {
		t.Fatal(err)
	}
	entry, err := retention.Snapshot(root, b.Lease.Execution.ID)
	if err != nil {
		t.Fatal(err)
	}
	exit := 7
	if err = history.Seal(tx, b.Lease.Execution.ID, entry, 19, &exit, false); err != nil {
		t.Fatal(err)
	}
	var locks transferCaptures
	defer locks.close()
	out, err := snapshotTransferHistory(context.Background(), root, b, tx, &locks)
	if err != nil || len(out) != 1 || out[0].File == nil {
		t.Fatal("sealed source capture was not bound", err)
	}
	if err = os.WriteFile(b.Lease.Execution.Log, []byte("changed run output!"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = locks.check(root); err == nil {
		t.Fatal("changed capture was accepted after approval")
	}
	after, err := history.Get(tx, b.Lease.Execution.ID)
	if err != nil || after.ExitCode == nil || *after.ExitCode != 7 || !after.ReleasedAt.IsZero() {
		t.Fatal("failed recheck changed retained history", err)
	}
}
