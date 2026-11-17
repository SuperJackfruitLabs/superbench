// Package workspacereclaim returns a released Fresh workspace's storage. The
// daemon decides from its own records that the workspace is released, idle,
// clean at its checkpoint and durable in its mirror and remote, records it as
// reclaiming, and has the fixed helper delete its payload-private tree, as the
// payload account, and retire its quota project. A legacy workspace is refused
// unchanged: archive and reap remain its route.
package workspacereclaim

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"regexp"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshtree"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/serviceidentity"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
)

var (
	// ErrConflict refuses a reclaim the workspace's own state does not allow.
	// Nothing was recorded or deleted.
	ErrConflict = errors.New("workspace reclaim refused")
	// ErrLegacy refuses a workspace without a Fresh registration.
	ErrLegacy = errors.New("workspace reclaim is for Fresh workspaces; archive and reap a legacy workspace")
)

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)

// Manager reclaims under the daemon's host lock. Fresh, Admit, Released and
// Helper default to the installed routes; tests replace them.
type Manager struct {
	State, Data string
	Timeout     time.Duration
	Within      func(func(workspace.Update) error) error
	Git         func(context.Context, string, ...string) (string, error)
	Fresh       freshtree.Selector
	// Admit refuses while any operation is admitted or bound for the key.
	Admit func(context.Context, *sql.Tx, *model.Bench) error
	// Released proves the tree clean at the complete checkpoint and that
	// checkpoint durable in the mirror and on the remote.
	Released func(context.Context, *model.Bench) error
	// Helper runs the fixed helper's reclaim for a reclaiming record.
	Helper func(context.Context, *model.Bench, payloadbootstrap.Spec) error
}

func refuse(why string) error { return errors.Join(ErrConflict, errors.New(why)) }

// Reclaim records a released Fresh workspace as reclaiming, then runs the
// fixed helper's reclaim and records it reclaimed. reclaiming is one-way:
// once recorded the workspace never returns to ready, and the same call
// resumes it after a crash, a refusal or a lost reply. A reclaimed workspace
// is returned unchanged.
func (m Manager) Reclaim(ctx context.Context, key string) (*model.Bench, error) {
	if !keyPattern.MatchString(key) || m.Within == nil || m.Git == nil && m.Released == nil {
		return nil, errors.New("workspace reclaim requires a valid key, storage and managed Git")
	}
	if _, scoped := operationcontrol.FromContext(ctx); scoped {
		return nil, operationcontrol.ErrAuthority
	}
	var committed *model.Bench
	err := m.Within(func(update workspace.Update) error {
		if err := update(func(rows benchstate.Database, tx *sql.Tx) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			b := rows[key]
			if b == nil || b.Workspace == nil {
				return refuse("no workspace with this key")
			}
			if err := workspacepolicy.Validate(b); err != nil {
				return err
			}
			if b.Path != filepath.Join(m.Data, "benches", key) {
				return errors.New("workspace root differs from configured data")
			}
			fresh, err := freshtree.Selected(m.Fresh, m.State, b)
			if err != nil {
				return err
			}
			if !fresh {
				return ErrLegacy
			}
			switch b.Workspace.Phase {
			case "reclaiming", "reclaimed":
				committed = workspace.Clone(b)
				return nil
			case "ready":
			default:
				return refuse("workspace is not ready: finish initialization first")
			}
			if err = m.ready(ctx, tx, b); err != nil {
				return err
			}
			candidate := workspace.Clone(b)
			candidate.Workspace.Phase = "reclaiming"
			rows[key] = candidate
			committed = workspace.Clone(candidate)
			return ctx.Err()
		}); err != nil {
			return err
		}
		if committed.Workspace.Phase == "reclaimed" {
			return nil
		}
		spec, err := Specification(committed)
		if err != nil {
			return err
		}
		helper := m.Helper
		if helper == nil {
			helper = Installed(m.State, m.Data)
		}
		if err = helper(ctx, workspace.Clone(committed), spec); err != nil {
			return err
		}
		return update(func(rows benchstate.Database, tx *sql.Tx) error {
			b := rows[key]
			if !reflect.DeepEqual(b, committed) {
				return errors.New("reclaiming workspace record changed during its reclaim")
			}
			finished := workspace.Clone(b)
			finished.Workspace.Phase = "reclaimed"
			rows[key] = finished
			committed = workspace.Clone(finished)
			return nil
		})
	})
	return workspace.Clone(committed), err
}

// ready is the decision a reclaim makes from the daemon's own records before
// recording anything: released (no lease), idle (no pending or staged work,
// no operation admitted), allocated, and its last checkpoint complete, clean
// in the tree and durable.
func (m Manager) ready(ctx context.Context, tx *sql.Tx, b *model.Bench) error {
	w := b.Workspace
	switch {
	case b.Lease != nil:
		return refuse("workspace has a writer; release it first")
	case w.Pending != nil || w.Stage != nil:
		return refuse("workspace has an unfinished checkpoint or stage")
	case w.Checkpoint == nil:
		return refuse("workspace has no complete checkpoint")
	case w.Archive != nil || w.Transfer != nil || w.InitializationRepair != nil:
		return refuse("workspace is archived, transferring or under repair")
	case b.ProjectID == 0:
		return refuse("workspace has no quota allocation")
	}
	admit := m.Admit
	if admit == nil {
		admit = func(ctx context.Context, tx *sql.Tx, b *model.Bench) error {
			return operationcontrol.AdmitContext(ctx, tx, b.Key, b.Workspace.ID)
		}
	}
	if err := admit(ctx, tx, b); err != nil {
		return errors.Join(ErrConflict, err)
	}
	released := m.Released
	if released == nil {
		released = m.released
	}
	return released(ctx, b)
}

// released inspects the tree through the payload helper, as release does, and
// confirms the checkpoint against the mirror and the remote task branches.
func (m Manager) released(ctx context.Context, b *model.Bench) error {
	l := workspace.Lifecycle{State: m.State, Data: m.Data, Timeout: m.Timeout, Git: m.Git, Fresh: m.Fresh}
	vector, err := l.Inspect(ctx, b, true)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(vector, b.Workspace.Checkpoint.Members) {
		return refuse("workspace HEAD vector differs from its complete checkpoint")
	}
	return l.Confirm(ctx, b)
}

// Specification is the canonical bootstrap Spec of a reclaiming workspace: the
// same source vector its create sent, which the helper binds to the retained
// bootstrap intent.
func Specification(b *model.Bench) (payloadbootstrap.Spec, error) {
	var zero payloadbootstrap.Spec
	if b == nil || b.Workspace == nil || !workspacepolicy.Reclaim(b.Workspace.Phase) {
		return zero, ErrConflict
	}
	w := b.Workspace
	s := payloadbootstrap.Spec{Version: payloadbootstrap.Version, Key: b.Key, ID: w.ID, CheckpointRepository: w.CheckpointRepository, QuotaBytes: b.RequestedQuotaBytes}
	for _, member := range w.Members {
		s.Members = append(s.Members, payloadbootstrap.Member{Name: member.Name, Repository: member.Repository, Branch: member.Branch, Source: member.Source, ObjectFormat: member.ObjectFormat})
	}
	if err := s.Validate(); err != nil {
		return zero, errors.Join(ErrConflict, err)
	}
	return s, nil
}

// Installed is the fixed helper route: the installed service identity, its
// host policy and the quota helper's reclaim-payload step.
func Installed(state, data string) func(context.Context, *model.Bench, payloadbootstrap.Spec) error {
	return func(ctx context.Context, b *model.Bench, spec payloadbootstrap.Spec) error {
		authority, err := serviceidentity.ResolveService()
		if err != nil {
			return err
		}
		p, err := authority.Policy()
		if err != nil || p.StateRoot != state || p.DataRoot != data {
			return errors.Join(errors.New("installed service identity differs from this daemon's storage"), err)
		}
		config, err := (hoststorage.Worker{State: state, Data: data}).Config()
		if err != nil || config == nil || config.PolicyOverride != "" {
			return errors.Join(errors.New("workspace reclaim requires the installed host policy"), err)
		}
		input, err := spec.Bytes()
		if err != nil {
			return err
		}
		if _, err = quotahelper.Reclaim(ctx, quotahelper.Config{State: state, Data: data, Command: config.QuotaHelper}, b.Key, b.Workspace.ID, b.ProjectID, input); err != nil {
			return err
		}
		return authority.Recheck()
	}
}
