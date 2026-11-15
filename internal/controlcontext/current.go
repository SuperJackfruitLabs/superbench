package controlcontext

import (
	"database/sql"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
)

// RequireCurrent refuses absent or historical state without creating or upgrading it.
func RequireCurrent(state string) error {
	return statestore.Read(state, func(db *sql.DB) error {
		if db == nil {
			return operationcontrol.ErrAuthority
		}
		return benchstate.CheckVersion(db)
	})
}
