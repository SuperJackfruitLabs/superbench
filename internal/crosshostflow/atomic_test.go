package crosshostflow

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/handoffjournal"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
)

func sqlChange(t *testing.T, state string, fn func(*sql.Tx) error) error {
	t.Helper()
	db, err := statestore.Open(filepath.Join(state, "bench.db"), false)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
func TestIgnoredCoreSourceExclusionDoesNotCommitExportAuthority(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	source, dest := Manager{Store: f.source}, Manager{Store: f.dest}
	o, err := dest.Offer(ctx, f.spec.Key, "import-create", 1<<20)
	must(t, err)
	o.Destination.HostID = strings.Repeat("e", 64)
	p, err := source.InspectExport(ctx, f.spec.Key, "export", *o)
	must(t, err)
	must(t, sqlChange(t, f.source.State, func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TRIGGER ignore_exclusion BEFORE UPDATE ON benches WHEN NEW.key='card_same' BEGIN SELECT RAISE(IGNORE); END`)
		return err
	}))
	if _, err = source.BeginExport(ctx, *p); err == nil {
		t.Fatal("export acknowledged despite ignored permanent core exclusion")
	}
	must(t, sqlChange(t, f.source.State, func(tx *sql.Tx) error {
		if _, err := handoffjournal.ExportByID(tx, "export"); err != sql.ErrNoRows {
			t.Fatal("export journal survived core rollback", err)
		}
		return nil
	}))
}
