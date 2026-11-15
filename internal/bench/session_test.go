package bench

import (
	"database/sql"
	"errors"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
)

func TestSessionPreservesCommittedOwnershipAfterLaterPersistenceFailure(t *testing.T) {
	s, remote := fixture(t)
	if _, err := s.Create("task", Options{Repo: remote}, false); err != nil {
		t.Fatal(err)
	}
	lease, err := newLease("initialization")
	if err != nil {
		t.Fatal(err)
	}
	err = s.session(func(session *statestore.Session) error {
		if err := s.update(session, func(records database, _ *sql.Tx) error {
			records["task"].Lease = lease
			return nil
		}); err != nil {
			return err
		}
		// A later phase can fail while writing its result. Its rollback must
		// retain the previously committed initialization owner, not publish a
		// half-updated record or roll the entire session back.
		failure := s.update(session, func(records database, tx *sql.Tx) error {
			if _, err := tx.Exec("CREATE TRIGGER reject_later_update BEFORE UPDATE ON benches BEGIN SELECT RAISE(ABORT, 'injected persistence failure'); END"); err != nil {
				return err
			}
			records["task"].Lease.Holder = "later-phase"
			return nil
		})
		if failure == nil {
			return errors.New("later write unexpectedly succeeded")
		}
		return s.update(session, func(records database, _ *sql.Tx) error {
			b := records["task"]
			if b.Lease == nil || b.Lease.Token != lease.Token || b.Lease.Holder != "initialization" {
				return errors.New("committed initialization ownership was lost")
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	// Reopen through the normal read path, outside the writer session.
	rows, err := s.Status("task")
	if err != nil || len(rows) != 1 || rows[0].Lease == nil || rows[0].Lease.Holder != "initialization" || rows[0].Lease.Token != "" {
		t.Fatalf("durable/redacted ownership differs: %+v, %v", rows, err)
	}
}
