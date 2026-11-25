// Package remotestate keeps a lease's remote writer sessions as the lease's
// remote execution: one scope recorded on Lease.Execution, granted under the
// lease token, re-checked on every admission and fenced before the lease moves.
// Claims carry lease identity only; a lease token never leaves this package.
package remotestate

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"math"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteca"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceprep"
)

var (
	ErrConflict    = errors.New("remote access conflicts with the workspace's state")
	ErrRevoked     = errors.New("remote access revoked")
	ErrUnavailable = errors.New("remote access unavailable")
)

// Scopes is the host's cgroup authority. Present reports whether a scope
// exists on this boot; execution.Host implements it.
type Scopes interface {
	Create(root, id string) (execution.Identity, error)
	Fence(*execution.Execution) error
	Empty(*execution.Execution) error
	Present(*execution.Execution) (bool, error)
}

type Manager struct {
	Prep   workspaceprep.Manager
	Scopes Scopes
	Now    func() time.Time
}

// ForStore binds the store's state authority. Nil scopes means the host.
func ForStore(store bench.Store, scopes Scopes) (Manager, error) {
	repair, err := store.WorkspaceRepair()
	if err != nil {
		return Manager{}, err
	}
	if scopes == nil {
		scopes = execution.Host{}
	}
	return Manager{Prep: repair.Prep, Scopes: scopes, Now: time.Now}, nil
}

func (m Manager) valid() error {
	if m.Prep.Update == nil || m.Prep.Read == nil || m.Prep.Gate == nil || m.Scopes == nil {
		return ErrUnavailable
	}
	return nil
}
func (m Manager) now() time.Time {
	if m.Now == nil {
		return time.Now().UTC()
	}
	return m.Now().UTC()
}

// root is where workspaceprep creates its scopes, so one delegated cgroup
// root holds every execution a lease records.
func (m Manager) root() string {
	if m.Prep.CgroupRoot == "" {
		return "/sys/fs/cgroup/superbench"
	}
	return m.Prep.CgroupRoot
}

func owns(l *model.Lease, token string) bool {
	return l != nil && token != "" && subtle.ConstantTimeCompare([]byte(l.Token), []byte(token)) == 1
}

// Grant is a writer grant: Claims name the lease and its remote execution.
type Grant struct {
	Key, Holder string
	// WorkingDirectory is the bench path a session chdirs into: the same
	// path bench-payload resolves from the policy data root and the key.
	WorkingDirectory string
	Claims           remoteca.Claims
}

// grantable admits a writer grant: the token owns an operational ready lease
// with no bound operation and no execution other than a remote one.
func grantable(rows benchstate.Database, tx operationcontrol.Reader, key, token string) (*model.Bench, error) {
	b := rows[key]
	if b == nil || b.Workspace == nil {
		return nil, bench.ErrNotFound
	}
	if !owns(b.Lease, token) {
		return nil, bench.ErrLease
	}
	w, l := b.Workspace, b.Lease
	if w.Phase != "ready" || w.Pending != nil || w.Archive != nil || w.Transfer != nil || w.InitializationRepair != nil || w.Stage != nil ||
		l.Holder == "initialization" || l.StopRequested || l.Continuation == "" || l.Execution != nil && l.Execution.Kind != execution.KindRemote {
		return nil, ErrConflict
	}
	if l.ID == "" || l.Generation == 0 || l.Generation > math.MaxInt64 {
		return nil, ErrUnavailable
	}
	if err := operationcontrol.RequireContext(tx, b, nil); err != nil {
		return nil, errors.Join(ErrConflict, err)
	}
	return b, nil
}

func clone(e *execution.Execution) *execution.Execution {
	if e == nil {
		return nil
	}
	c := *e
	return &c
}
func sameScope(a, b *execution.Execution) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

// Grant records the lease's remote execution, or reuses the recorded one while
// its scope is present on this boot. A recorded scope that is gone (benchd
// restarted, or the host rebooted) is fenced and replaced under a new ID, so
// claims issued for it no longer admit. A refusal calls no scope.
func (m Manager) Grant(ctx context.Context, key, token string) (Grant, error) {
	if err := m.valid(); err != nil {
		return Grant{}, err
	}
	var g Grant
	err := m.Prep.Gate(key, func() error {
		var prior *execution.Execution
		if err := m.Prep.Update(func(rows benchstate.Database, tx *sql.Tx) error {
			b, err := grantable(rows, tx, key, token)
			if err == nil {
				prior = clone(b.Lease.Execution)
			}
			return err
		}); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		live := false
		if prior != nil && prior.Phase == "running" {
			present, err := m.Scopes.Present(prior)
			if err != nil {
				return errors.Join(ErrUnavailable, err)
			}
			live = present
		}
		var created *execution.Execution
		if !live {
			if prior != nil {
				if err := m.Scopes.Fence(prior); err != nil {
					return errors.Join(ErrUnavailable, err)
				}
			}
			id, err := model.Nonce()
			if err != nil {
				return err
			}
			identity, err := m.Scopes.Create(m.root(), id)
			if err != nil {
				return errors.Join(ErrUnavailable, err)
			}
			created = &execution.Execution{ID: id, Root: m.root(), BootID: identity.BootID, HostID: identity.HostID, Phase: "running", Kind: execution.KindRemote}
		}
		err := m.Prep.Update(func(rows benchstate.Database, tx *sql.Tx) error {
			b, err := grantable(rows, tx, key, token)
			if err != nil {
				return err
			}
			if !sameScope(b.Lease.Execution, prior) {
				return ErrConflict
			}
			if created != nil {
				b.Lease.Execution = clone(created)
			}
			if !payloadpolicy.Directory(b.Path) {
				return ErrUnavailable
			}
			g = Grant{Key: key, Holder: b.Lease.Holder, WorkingDirectory: b.Path, Claims: remoteca.Claims{BenchID: b.Workspace.ID, LeaseID: b.Lease.ID, Generation: b.Lease.Generation, ExecutionID: b.Lease.Execution.ID}}
			return nil
		})
		if err != nil && created != nil {
			err = errors.Join(err, m.Scopes.Fence(created))
		}
		return err
	})
	if err != nil {
		return Grant{}, err
	}
	return g, nil
}
