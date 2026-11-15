// Package mirroradmin binds reviewed staging collection to host administration.
package mirroradmin

import (
	"context"
	"errors"
	"path/filepath"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/mirrorstage"
)

func Manager(store bench.Store) mirrorstage.Manager {
	return mirrorstage.Manager{
		State: store.State, Parent: filepath.Join(store.Data, "mirrors"),
		Within: store.LocalAdministration,
		VerifyControl: func(ctx context.Context, state, root string) error {
			if e := ctx.Err(); e != nil {
				return e
			}
			actual, e := filepath.EvalSymlinks(store.State)
			if e != nil || state != actual || !gitcontrol.Available(root) {
				return errors.New("staging collection requires its original managed Linux Git control root")
			}
			// LocalAdministration already reconciled every durable control intent
			// while acquiring this same host lock. Never infer a fence from age,
			// a process PID, a directory lock or an ordinary command's exit.
			return nil
		},
	}
}
