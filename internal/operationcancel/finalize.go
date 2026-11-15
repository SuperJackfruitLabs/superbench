package operationcancel

import (
	"database/sql"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

// Finish reconciles the post-receipt crash window without redispatching work.
// Cutoff must independently verify the original immutable completed receipt;
// ordinary context completion and response arrival are not this authority.
func (m Manager) Finish(i operationcontrol.Intent, cutoff func() error) error {
	if !i.Valid() || m.Update == nil || cutoff == nil {
		return operationcontrol.ErrAuthority
	}
	return m.Update(func(_ benchstate.Database, tx *sql.Tx) error {
		if err := cutoff(); err != nil {
			return err
		}
		return operationcontrol.Finish(tx, i)
	})
}
