package operationcancel

import (
	"database/sql"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

// NoEffects proves that the closed target never entered the engine. The caller
// separately proves an empty tracked helper inventory and the same durable
// cancel barrier. Existing or orphaned provenance is never adopted or fenced.
func (m Manager) NoEffects(t operationcontrol.Target) error {
	if !t.Valid() || m.Update == nil || m.CheckBarrier == nil {
		return operationcontrol.ErrAuthority
	}
	return m.Update(func(_ benchstate.Database, tx *sql.Tx) error {
		if err := m.CheckBarrier(); err != nil {
			return err
		}
		if _, err := operationcontrol.Get(tx, t); err != sql.ErrNoRows {
			if err != nil {
				return err
			}
			return operationcontrol.ErrAuthority
		}
		var runs int
		if err := tx.QueryRow(`SELECT count(*) FROM workspace_operation_runs WHERE operation_id=?`, t.ID()).Scan(&runs); err != nil {
			return err
		}
		if runs != 0 {
			return operationcontrol.ErrAuthority
		}
		if err := gitcontrol.NoOperation(m.State, t); err != nil {
			return err
		}
		return m.CheckBarrier()
	})
}
