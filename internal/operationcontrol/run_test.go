package operationcontrol

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func TestHistoryIntentPinsOnlyItsActualOperationRunAtomically(t *testing.T) {
	db := store(t)
	i, b := intent("run-history"), record()
	state := t.TempDir()
	state, err := filepath.EvalSymlinks(state)
	if err != nil {
		t.Fatal(err)
	}
	c := Control{State: state, Root: "/scope", HostID: strings.Repeat("f", 64), BootID: "boot"}
	ctx, err := WithScope(context.Background(), i, func(fn func() error) error { return fn() })
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = WithControl(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	commit(t, db, func(tx *sql.Tx) error {
		if err := history.Schema(tx); err != nil {
			return err
		}
		if err := BeginWithControl(tx, i, b.Workspace.ID, c); err != nil {
			return err
		}
		return Bind(tx, i, b)
	})
	b.Lease.Execution = &model.Execution{ID: strings.Repeat("1", 32), Root: c.Root, HostID: c.HostID, BootID: c.BootID, Phase: "starting", Log: filepath.Join(state, "run-"+strings.Repeat("1", 32)+".log"), LogLimitBytes: 1024}
	r := history.Record{ID: b.Lease.Execution.ID, Key: b.Key, Holder: b.Lease.Holder, StartedAt: time.Now().UTC()}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = StartHistory(ctx, tx, b, r); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	if _, err = history.Get(db, r.ID); err != sql.ErrNoRows {
		t.Fatal("rollback retained history", err)
	}
	if _, err = GetRun(db, i, r.ID); err != sql.ErrNoRows {
		t.Fatal("rollback retained operation authority", err)
	}
	commit(t, db, func(tx *sql.Tx) error { return StartHistory(ctx, tx, b, r) })
	got, err := GetRun(db, i, r.ID)
	if err != nil || got.Execution != *b.Lease.Execution {
		t.Fatal(got, err)
	}
	saved, err := history.Get(db, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := history.Digest(saved)
	if err != nil || got.PriorDigest != digest {
		t.Fatal(got, err)
	}
	other := i
	other.Epoch = strings.Repeat("2", 32)
	if _, err = GetRun(db, other, r.ID); !errors.Is(err, ErrAuthority) {
		t.Fatal("run adopted by later epoch", err)
	}
	b.Lease.Execution.ID = strings.Repeat("3", 32)
	if _, err = GetRun(db, i, b.Lease.Execution.ID); err != sql.ErrNoRows {
		t.Fatal("older or arbitrary history acquired provenance", err)
	}
}

func TestRunProvenanceFailureCannotCommitHistoryWithoutAuthority(t *testing.T) {
	for _, fault := range []string{"ignored_insert", "capacity", "scope_changed"} {
		t.Run(fault, func(t *testing.T) {
			db := store(t)
			i, b := intent("run-fault"), record()
			c := Control{State: "/state", Root: "/scope", HostID: strings.Repeat("f", 64), BootID: "boot"}
			ctx, err := WithScope(context.Background(), i, func(fn func() error) error { return fn() })
			if err != nil {
				t.Fatal(err)
			}
			ctx, err = WithControl(ctx, c)
			if err != nil {
				t.Fatal(err)
			}
			commit(t, db, func(tx *sql.Tx) error {
				if err := history.Schema(tx); err != nil {
					return err
				}
				if err := BeginWithControl(tx, i, b.Workspace.ID, c); err != nil {
					return err
				}
				return Bind(tx, i, b)
			})
			switch fault {
			case "ignored_insert":
				_, err = db.Exec(`CREATE TRIGGER refuse_run BEFORE INSERT ON workspace_operation_runs BEGIN SELECT RAISE(IGNORE); END`)
			case "capacity":
				_, err = db.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<?) INSERT INTO workspace_operation_runs SELECT ?,?,'prior-'||x,'{}','unused' FROM n`, MaxRuns, i.Target.ID(), i.Epoch)
			}
			if err != nil {
				t.Fatal(err)
			}
			b.Lease.Execution = &model.Execution{ID: strings.Repeat("1", 32), Root: c.Root, HostID: c.HostID, BootID: c.BootID, Phase: "starting", Log: "/state/run-" + strings.Repeat("1", 32) + ".log", LogLimitBytes: 1024}
			if fault == "scope_changed" {
				b.Lease.Execution.Root = "/other"
			}
			r := history.Record{ID: b.Lease.Execution.ID, Key: b.Key, Holder: b.Lease.Holder, StartedAt: time.Now().UTC()}
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if err = StartHistory(ctx, tx, b, r); err == nil {
				t.Fatal("missing scope provenance acknowledged")
			}
			tx.Rollback()
			if _, err = history.Get(db, r.ID); err != sql.ErrNoRows {
				t.Fatal("failed intent left history", err)
			}
			if _, err = GetRun(db, i, r.ID); err != sql.ErrNoRows {
				t.Fatal("failed scope saved authority", err)
			}
		})
	}
}
