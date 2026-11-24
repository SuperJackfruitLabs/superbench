// Package remotessh is benchd's SSH door to a bench. This file holds the
// launcher contract the session launcher implements.
package remotessh

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
