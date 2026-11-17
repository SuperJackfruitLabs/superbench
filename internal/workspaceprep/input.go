package workspaceprep

import (
	"context"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

// A legacy checkout is read directly. A Fresh payload-private checkout is read
// and run only through its freshtree route (m.Fresh); the daemon never opens it.
func (m Manager) lifecycle() workspace.Lifecycle {
	return workspace.Lifecycle{State: m.State, Data: m.Data, Git: m.Git, Fresh: m.Fresh}
}

// Prepared is CheckPrepared through this manager's tree selection.
func (m Manager) Prepared(b *model.Bench) error { return m.checkPrepared(context.Background(), b) }
