package workspace

import (
	"context"
	"database/sql"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

func (l Lifecycle) ownerContext(ctx context.Context, tx *sql.Tx, b *model.Bench, token string) error {
	if err := operationcontrol.GuardContext(ctx, tx, b); err != nil {
		return err
	}
	return l.owner(b, token)
}
func (l Lifecycle) initializationContext(ctx context.Context, tx *sql.Tx, b *model.Bench, token string) error {
	if err := operationcontrol.GuardContext(ctx, tx, b); err != nil {
		return err
	}
	return l.initialization(b, token)
}
