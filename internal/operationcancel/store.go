package operationcancel

import (
	"database/sql"
	"path/filepath"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statepath"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
)

// Persistent requires the already-installed current schema. Cancellation never
// migrates unknown older activity or invokes global Git recovery: only the
// exact operation's selective recovery may fence controls under this session.
func Persistent(state, data string, backend Backend, gate func(string, string, func() error) error) (Manager, error) {
	if backend == nil || gate == nil {
		return Manager{}, operationcontrol.ErrAuthority
	}
	if err := statepath.Validate(state, data); err != nil {
		return Manager{}, err
	}
	physical, err := filepath.EvalSymlinks(state)
	if err != nil {
		return Manager{}, err
	}
	m := Manager{State: physical, Backend: backend, Gate: func(key string, fn func() error) error { return gate(physical, key, fn) }}
	m.Update = func(fn func(benchstate.Database, *sql.Tx) error) error {
		return statestore.Within(physical, nil, func(session *statestore.Session) error {
			return session.Update(func(tx *sql.Tx) error {
				if err := benchstate.CheckVersion(tx); err != nil {
					return err
				}
				return benchstate.Update(tx, fn)
			})
		})
	}
	return m, nil
}
