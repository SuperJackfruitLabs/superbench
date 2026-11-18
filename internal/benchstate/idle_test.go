package benchstate

import (
	"database/sql"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

// Releasing the writer records when the idle grace began; holding the lease,
// acquiring it, or an unrelated update leaves the timestamp alone.
func TestReleaseRecordsIdleSince(t *testing.T) {
	leased := readyAggregate(t)
	leased.Lease, leased.Workspace.IdleSince = operationalLease(), "2026-01-01T00:00:00Z"
	db := oldDatabase(t, 27, leased)
	if err := migrateForTest(db, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	step := func(change func(*model.Bench)) *model.Bench {
		t.Helper()
		var out *model.Bench
		tx, err := db.Begin()
		if err == nil {
			err = Update(tx, func(rows Database, _ *sql.Tx) error { out = rows["card"]; change(out); return nil })
		}
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if b := step(func(*model.Bench) {}); b.Workspace.IdleSince != "2026-01-01T00:00:00Z" {
		t.Fatalf("an update that kept the writer changed IdleSince to %q", b.Workspace.IdleSince)
	}
	before := time.Now().UTC()
	b := step(func(b *model.Bench) { b.Lease = nil })
	at, err := time.Parse(time.RFC3339Nano, b.Workspace.IdleSince)
	if err != nil || at.Before(before) || at.After(time.Now().UTC()) {
		t.Fatalf("release recorded IdleSince %q", b.Workspace.IdleSince)
	}
	if again := step(func(*model.Bench) {}); again.Workspace.IdleSince != b.Workspace.IdleSince {
		t.Fatal("an update without a writer moved IdleSince")
	}
	if acquired := step(func(b *model.Bench) { b.Lease = operationalLease(); b.Workspace.Preparation = nil }); acquired.Workspace.IdleSince != b.Workspace.IdleSince {
		t.Fatal("acquisition moved IdleSince")
	}
}
