package workspaceprep

import (
	"context"
	"database/sql"
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

var ErrPreparation = errors.New("workspace requires complete current preparation evidence")

// VerifyPrepared checks physical identities, current inputs, owned sealed history
// and stopped ownership together. Partial Prepare success is not a handoff.
func (m Manager) VerifyPrepared(ctx context.Context, key, token string) (*model.Bench, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	err := m.Gate(key, func() error {
		return m.Update(func(rows benchstate.Database, tx *sql.Tx) error {
			b, err := owned(rows, key, token)
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := m.preparable(b); err != nil {
				return err
			}
			if err := m.stopped(b); err != nil {
				return err
			}
			if err := m.inspectPayload(ctx, b); err != nil {
				return err
			}
			return prepared(tx, b)
		})
	})
	b, readErr := m.ownedCurrent(key, token)
	if err != nil {
		err = errors.Join(ErrPreparation, err)
	}
	return b, errors.Join(err, readErr)
}

// CompleteSetup requires the whole vector before publishing a checkpoint and
// repeats that proof inside the final lease-release transaction. Ordinary repair
// release intentionally does not require preparation for every member.
func (m Manager) CompleteSetup(ctx context.Context, l workspace.Lifecycle, key, token string) (*model.Bench, error) {
	b, err := m.VerifyPrepared(ctx, key, token)
	if err != nil {
		return b, err
	}
	checkpointed, err := l.Checkpoint(ctx, key, token)
	if checkpointed != nil {
		b = checkpointed
	}
	if err != nil {
		return b, err
	}
	l.Released = func(tx *sql.Tx, current *model.Bench) error {
		if err := prepared(tx, current); err != nil {
			return errors.Join(ErrPreparation, err)
		}
		return ReleaseHistory(tx, current)
	}
	released, err := l.Release(ctx, key, token)
	if released != nil {
		b = released
	}
	return b, err
}
