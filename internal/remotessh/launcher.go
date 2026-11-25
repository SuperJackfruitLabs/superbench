// Package remotessh is benchd's SSH door to a bench: a certificate-only SSH
// server whose only channel is a session that runs a shell, one command or
// SFTP through a Launcher, inside the lease's remote execution.
package remotessh

import (
	"context"
	"os"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteschema"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotestate"
)

// SessionSpec is what one channel asked for. Mode is "shell", "exec" or
// "sftp"; Command only with exec; a PTY always carries its Term.
type SessionSpec struct {
	Mode, Command, Term string
	PTY                 bool
	Cols, Rows          uint32
}

// Process is a started session. Terminate sends SIGTERM, then SIGKILL after
// five seconds; Wait reports the exit code or the terminating signal's name.
type Process interface {
	Wait() (exitCode int, signal string, err error)
	Terminate()
}

// Launcher starts one session for an admitted lease. It owns fds and closes
// them on every path: one PTY slave, or the stdin read end and the stdout and
// stderr write ends of three pipes.
type Launcher interface {
	Start(ctx context.Context, a remotestate.Admission, spec SessionSpec, fds []*os.File) (Process, error)
}

// Audit records sessions. SessionStarted re-admits the lease in the same
// transaction; SessionEnded keeps the first end reason and only gains counts.
type Audit interface {
	SessionStarted(a remotestate.Admission, r remoteschema.SessionRecord) (int64, error)
	SessionEnded(id int64, in, out uint64, reason string) error
}
