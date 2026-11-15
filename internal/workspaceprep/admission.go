package workspaceprep

import (
	"context"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reftransfer"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/transferguard"
)

func (m Manager) transferAdmission(ctx context.Context, db reftransfer.Reader, b *model.Bench) error {
	return transferguard.Check(ctx, transferguard.Config{State: m.State, Data: m.Data, Git: m.Git}, db, b)
}
