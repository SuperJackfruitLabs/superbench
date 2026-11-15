package workspaceprep

import (
	"context"
	"database/sql"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reftransfer"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/transferguard"
)

func (m Manager) transferAdmission(ctx context.Context, db reftransfer.Reader, b *model.Bench) error {
	if err := operationcontrol.GuardContext(ctx, db, b); err != nil {
		return err
	}
	return transferguard.Check(ctx, transferguard.Config{State: m.State, Data: m.Data, Git: m.Git}, db, b)
}

func (m Manager) enterOperation(ctx context.Context, key, token string) error {
	return m.Update(func(rows benchstate.Database, tx *sql.Tx) error {
		b, err := owned(rows, key, token)
		if err != nil {
			return err
		}
		if err = m.preparable(b); err != nil {
			return err
		}
		if err = operationcontrol.AdmitContext(ctx, tx, key, b.Workspace.ID); err != nil {
			return err
		}
		return operationcontrol.BindContext(ctx, tx, b)
	})
}
