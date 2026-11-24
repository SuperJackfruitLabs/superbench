//go:build !linux

package remotesession

import (
	"context"
	"os"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotessh"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotestate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/serviceidentity"
)

// Start refuses: the session helper exists only on Linux. It closes fds.
func (Installed) Start(_ context.Context, _ remotestate.Admission, _ remotessh.SessionSpec, fds []*os.File) (remotessh.Process, error) {
	closeAll(fds)
	return nil, serviceidentity.ErrAuthority
}
