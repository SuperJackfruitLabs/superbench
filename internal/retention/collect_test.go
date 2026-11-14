package retention

import (
	"database/sql"
	"errors"
	"fmt"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fixture(t *testing.T) (string, *sql.DB) {
	t.Helper()
	root := t.TempDir()
	if e := os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("sqlite", filepath.Join(root, "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	tx, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if e = history.Schema(tx); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	return root, db
}
func transaction(db *sql.DB, fn func(*sql.Tx) error) error {
	tx, e := db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = fn(tx); e != nil {
		return e
	}
	return tx.Commit()
}
func record(t *testing.T, root string, db *sql.DB, n int) history.Record {
	t.Helper()
	id := fmt.Sprintf("%032x", n)
	name, _ := Name(id)
	text := []byte(fmt.Sprintf("output-%d", n))
	if e := os.WriteFile(filepath.Join(root, name), text, 0600); e != nil {
		t.Fatal(e)
	}
	file, e := Snapshot(root, id)
	if e != nil {
		t.Fatal(e)
	}
	r := history.Record{ID: id, Key: "task", Holder: "worker", StartedAt: time.Now().UTC().Add(-48 * time.Hour), ReleasedAt: time.Now().UTC().Add(-24 * time.Hour), SealedAt: time.Now().UTC().Add(-24 * time.Hour), Checkpoint: "confirmed", File: &file, Size: int64(len(text))}
	if e = transaction(db, func(tx *sql.Tx) error { return history.Start(tx, r) }); e != nil {
		t.Fatal(e)
	}
	return r
}
func TestCollectionApprovalCrashResumeAndMetadataPruning(t *testing.T) {
	root, db := fixture(t)
	r := record(t, root, db, 1)
	p, e := Build(db, root, nil, 0, 0, 100)
	if e != nil {
		t.Fatal(e)
	}
	forged := *p
	forged.Digest = "wrong"
	if e = transaction(db, func(tx *sql.Tx) error { return Prepare(tx, root, forged, nil) }); e == nil {
		t.Fatal("wrong approval accepted")
	}
	if e = transaction(db, func(tx *sql.Tx) error { return Prepare(tx, root, *p, nil) }); e != nil {
		t.Fatal(e)
	}
	e = transaction(db, func(tx *sql.Tx) error {
		_, e := Apply(tx, root, *p, nil, func() error { return errors.New("crash after unlink") })
		return e
	})
	if e == nil {
		t.Fatal("fault ignored")
	}
	name, _ := Name(r.ID)
	if _, e = os.Stat(filepath.Join(root, name)); !os.IsNotExist(e) {
		t.Fatal("approved file was not removed")
	}
	prior, e := history.Get(db, r.ID)
	if e != nil || prior.Collection != p.Digest || !prior.CollectedAt.IsZero() {
		t.Fatal("durable pending intent lost", e)
	}
	for i := 0; i < 2; i++ {
		if e = transaction(db, func(tx *sql.Tx) error {
			c, e := Apply(tx, root, *p, nil, nil)
			if e == nil && (c.Status != "completed" || c.Count != 1) {
				return errors.New("bad completed result")
			}
			return e
		}); e != nil {
			t.Fatal(e)
		}
	}
	prune, e := Build(db, root, nil, 0, 0, 100)
	if e != nil || len(prune.Records) != 1 || len(prune.Collections) != 1 {
		t.Fatal("collected evidence cannot be pruned", e)
	}
	if e = transaction(db, func(tx *sql.Tx) error { return Prepare(tx, root, *prune, nil) }); e != nil {
		t.Fatal(e)
	}
	if e = transaction(db, func(tx *sql.Tx) error { _, e := Apply(tx, root, *prune, nil, nil); return e }); e != nil {
		t.Fatal(e)
	}
	if _, e = history.Get(db, r.ID); e != sql.ErrNoRows {
		t.Fatal("history remained after approved pruning", e)
	}
	if e = transaction(db, func(tx *sql.Tx) error { return Prepare(tx, root, *p, nil) }); e == nil {
		t.Fatal("old collection plan became executable after summary pruning")
	}
}
func TestHeldUnsealedWritersAndChangedFilesAreProtected(t *testing.T) {
	root, db := fixture(t)
	ready := record(t, root, db, 1)
	held := record(t, root, db, 2)
	unsealed := record(t, root, db, 3)
	if e := transaction(db, func(tx *sql.Tx) error {
		held.ReleasedAt = time.Time{}
		unsealed.File = nil
		unsealed.SealedAt = time.Time{}
		if e := history.Save(tx, held); e != nil {
			return e
		}
		return history.Save(tx, unsealed)
	}); e != nil {
		t.Fatal(e)
	}
	f, _, e := LockSnapshot(root, ready.ID)
	if e != nil {
		t.Fatal(e)
	}
	p, e := Build(db, root, nil, 0, 0, 100)
	if e != nil || len(p.Logs) != 0 {
		t.Fatal("open writer/held/unsealed log collected", e)
	}
	f.Close()
	p, e = Build(db, root, nil, 0, 0, 100)
	if e != nil || len(p.Logs) != 1 {
		t.Fatal(e)
	}
	if e = transaction(db, func(tx *sql.Tx) error { return Prepare(tx, root, *p, map[string]bool{ready.ID: true}) }); e == nil {
		t.Fatal("active execution metadata ignored")
	}
	name, _ := Name(ready.ID)
	if e = os.WriteFile(filepath.Join(root, name), []byte("changed"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = transaction(db, func(tx *sql.Tx) error { return Prepare(tx, root, *p, nil) }); e == nil {
		t.Fatal("changed file approved")
	}
	if _, e = os.Stat(filepath.Join(root, name)); e != nil {
		t.Fatal("refusal removed file", e)
	}
}
func TestCancelPreservesChangesAndRevokesOldApproval(t *testing.T) {
	root, db := fixture(t)
	r := record(t, root, db, 1)
	p, e := Build(db, root, nil, 0, 0, 100)
	if e != nil {
		t.Fatal(e)
	}
	if e = transaction(db, func(tx *sql.Tx) error { return Prepare(tx, root, *p, nil) }); e != nil {
		t.Fatal(e)
	}
	name, _ := Name(r.ID)
	path := filepath.Join(root, name)
	if e = os.WriteFile(path, []byte("changed"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = transaction(db, func(tx *sql.Tx) error { _, e := Apply(tx, root, *p, nil, nil); return e }); e == nil {
		t.Fatal("changed remaining file deleted")
	}
	if e = transaction(db, func(tx *sql.Tx) error { _, e := Cancel(tx, root, p.Digest, nil); return e }); e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile(path)
	if e != nil || string(data) != "changed" {
		t.Fatal("cancellation modified data", e)
	}
	if e = transaction(db, func(tx *sql.Tx) error { return Prepare(tx, root, *p, nil) }); e == nil {
		t.Fatal("canceled approval reused")
	}
	prune, e := Build(db, root, nil, 0, 0, 100)
	if e != nil || len(prune.Collections) != 1 {
		t.Fatal("cancel summary not pruneable", e)
	}
	if e = transaction(db, func(tx *sql.Tx) error { return Prepare(tx, root, *prune, nil) }); e != nil {
		t.Fatal(e)
	}
	if e = transaction(db, func(tx *sql.Tx) error { _, e := Apply(tx, root, *prune, nil, nil); return e }); e != nil {
		t.Fatal(e)
	}
	if e = transaction(db, func(tx *sql.Tx) error { return Prepare(tx, root, *p, nil) }); e == nil {
		t.Fatal("canceled approval revived after summary pruning")
	}
	got, e := history.Get(db, r.ID)
	if e != nil || got.Collection != "" || got.Revision != 1 {
		t.Fatal("cancellation did not revoke prepared revision", e)
	}
}
