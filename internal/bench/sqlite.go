package bench

import (
	"database/sql"
	"path/filepath"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
)

const schemaVersion = benchstate.SchemaVersion

type reader = benchstate.Reader

func (s Store) openSQL(readOnly bool) (*sql.DB, error) {
	return statestore.Open(filepath.Join(s.State, "bench.db"), readOnly)
}
func checkVersion(r reader) error         { return benchstate.CheckVersion(r) }
func validRecords(records database) error { return benchstate.Validate(records) }
func (s Store) legacy() (database, error) { return benchstate.Legacy(s.State) }
func (s Store) migrate(tx *sql.Tx) error {
	return benchstate.Migrate(tx, s.State, s.pinMigratedInodes)
}
func loadRecords(r reader) (database, error) { return benchstate.Load(r) }
func (s Store) Migrate() error               { return s.transaction(func(database) error { return nil }) }
