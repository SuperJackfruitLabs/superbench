// Package execution fences supervised processes on their recorded host.
package execution

type Execution struct {
	ID            string
	Root          string
	BootID        string
	HostID        string
	Phase         string // starting, running, stopped
	PID           int
	Log           string
	LogLimitBytes uint64
}

type Identity struct{ BootID, HostID string }
