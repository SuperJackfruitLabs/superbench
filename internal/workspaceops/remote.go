package workspaceops

import (
	"context"
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotestate"
)

// remoteFor opens the remote state manager; tests count its calls.
var remoteFor = remotestate.ForStore

// fenceRemote closes the lease's remote writer sessions, killing their scope
// and clearing the remote execution, before checkpoint, release, stop or
// stop_admin moves the lease. A failed fence refuses the operation.
//
// It first reads the row without a write session. Only a lease that records
// a remote execution opens the remote state manager and its gate, so with no
// remote execution (always, while remote access is off) these operations
// take no extra session. It does not ask whether remote access is on: an
// execution left from before an operator turned it off is still fenced.
func (m Manager) fenceRemote(ctx context.Context, key, token string, admin bool) error {
	if remote, err := m.recordsRemote(key); err == nil && !remote {
		return nil
	}
	r, err := remoteFor(m.Store, m.RemoteScopes)
	if err == nil {
		err = r.Fence(ctx, key, token, admin)
	}
	if errors.Is(err, remotestate.ErrConflict) {
		err = errors.Join(ErrConflict, err)
	}
	return err
}

// recordsRemote reports whether key's lease records a remote execution. An
// unknown bench records none; the operation reports it. Any other read error
// leaves the decision to the fence, which reads again under its gate.
func (m Manager) recordsRemote(key string) (bool, error) {
	rows, err := m.Store.Snapshot(key)
	if errors.Is(err, bench.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, b := range rows {
		if b.Lease != nil && b.Lease.Execution != nil && b.Lease.Execution.Kind == execution.KindRemote {
			return true, nil
		}
	}
	return false, nil
}
