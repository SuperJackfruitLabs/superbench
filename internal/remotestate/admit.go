package remotestate

import (
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteca"
)

// Admission is a connection's or channel's view of the live lease: the bench
// root sessions start in and the remote execution they belong to.
type Admission struct {
	Key, BenchID, LayoutID, Holder, Root string
	Claims                               remoteca.Claims
	Execution                            execution.Execution
}

// admitted finds the one bench the claims name and compares every claim with
// the live lease exactly. A rotated generation, a new acquisition or a
// replaced or fenced execution refuses the claims.
func admitted(rows benchstate.Database, c remoteca.Claims) (*model.Bench, error) {
	var found *model.Bench
	for _, b := range rows {
		if b != nil && b.Workspace != nil && c.BenchID != "" && b.Workspace.ID == c.BenchID {
			if found != nil {
				return nil, ErrRevoked
			}
			found = b
		}
	}
	if found == nil || found.Workspace.Phase != "ready" || found.Workspace.Pending != nil || found.Workspace.Transfer != nil {
		return nil, ErrRevoked
	}
	l := found.Lease
	if l == nil || l.ID == "" || l.ID != c.LeaseID || l.Generation != c.Generation || l.StopRequested {
		return nil, ErrRevoked
	}
	if e := l.Execution; e == nil || e.Kind != execution.KindRemote || e.Phase != "running" || e.ID == "" || e.ID != c.ExecutionID {
		return nil, ErrRevoked
	}
	return found, nil
}

func admission(b *model.Bench, c remoteca.Claims) Admission {
	return Admission{Key: b.Key, BenchID: b.Workspace.ID, LayoutID: b.Workspace.ID, Holder: b.Lease.Holder, Root: b.Path, Claims: c, Execution: *b.Lease.Execution}
}

// Admit re-checks claims against the live lease.
func (m Manager) Admit(c remoteca.Claims) (Admission, error) {
	if err := m.valid(); err != nil {
		return Admission{}, err
	}
	var a Admission
	err := m.Prep.Read(func(rows benchstate.Database) error {
		b, err := admitted(rows, c)
		if err == nil {
			a = admission(b, c)
		}
		return err
	})
	if errors.Is(err, ErrRevoked) {
		return Admission{}, ErrRevoked
	}
	if err != nil {
		return Admission{}, errors.Join(ErrUnavailable, err)
	}
	return a, nil
}
