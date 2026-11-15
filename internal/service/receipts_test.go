package service

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// Fault injection uses a separate connection to this disposable fixture DB; the
// production ledger API never exposes its database or admission mutex.
func receiptTestDB(t *testing.T, s *Server) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(s.config.Store.State, "service.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
