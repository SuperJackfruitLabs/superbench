package workspaceops

import (
	"context"
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotestate"
)

// fenceRemote closes the lease's remote writer sessions, killing their scope
// and clearing the remote execution, before checkpoint, release, stop or
// stop_admin moves the lease. A failed fence refuses the operation.
func (m Manager) fenceRemote(ctx context.Context, key, token string, admin bool) error {
	r, err := remotestate.ForStore(m.Store, m.RemoteScopes)
	if err == nil {
		err = r.Fence(ctx, key, token, admin)
	}
	if errors.Is(err, remotestate.ErrConflict) {
		err = errors.Join(ErrConflict, err)
	}
	return err
}
