package operationcontrol

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestPermanentTerminalBindingsDoNotConsumeActiveOperationBudget(t *testing.T) {
	db := store(t)
	commit(t, db, func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(`INSERT INTO workspace_operations(id,` + columns + `) VALUES(?,?,?,?,?,?,?,?,'','','','finished','','','','')`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for n := 0; n <= MaxOperations; n++ {
			i := intent(fmt.Sprintf("history-%05d", n))
			if _, err = stmt.Exec(i.Target.ID(), i.Target.UID, i.Target.RequestID, i.Target.Digest, i.Key, i.Operation, i.Epoch, ""); err != nil {
				return err
			}
		}
		return nil
	})
	next := intent("fresh")
	commit(t, db, func(tx *sql.Tx) error { return Begin(tx, next, "") })
	first := intent("history-00000")
	retained, err := Get(db, first.Target)
	if err != nil || retained.Intent != first || retained.Phase != "finished" {
		t.Fatal("new admission damaged historical binding", retained, err)
	}
	duplicate := next
	duplicate.Target.RequestID = "other"
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	err = Begin(tx, duplicate, "")
	tx.Rollback()
	if !errors.Is(err, ErrBusy) {
		t.Fatal("active same-key exclusion lost", err)
	}
	changed := first
	changed.Target.Digest = strings.Repeat("c", 64)
	if _, err = Get(db, changed.Target); !errors.Is(err, ErrConflict) {
		t.Fatal("historical identity adopted changed body", err)
	}
}
