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
	// Kind is empty for a supervised run or preparation stage. KindRemote is
	// the lease's remote writer sessions: a scope with no process of its own,
	// no log and no history, fenced and cleared before the lease moves on.
	Kind string `json:",omitempty"`
}

const KindRemote = "remote"

type Identity struct{ BootID, HostID string }
