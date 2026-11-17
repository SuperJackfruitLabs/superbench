package workspaceprep

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/cachepolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshtree"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/setup"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

type memberPlan struct {
	member model.WorkspaceMember
	config *setup.Config
	digest string
}

func receipt(b *model.Bench, member string) string {
	return "workspace:" + b.Workspace.ID + ":" + member
}

func proof(b *model.Bench, name string) *model.WorkspacePreparation {
	for _, p := range b.Workspace.Preparation {
		if p.Name == name {
			copy := p
			return &copy
		}
	}
	return nil
}

func removeProof(b *model.Bench, name string) {
	var kept []model.WorkspacePreparation
	for _, p := range b.Workspace.Preparation {
		if p.Name != name {
			kept = append(kept, p)
		}
	}
	b.Workspace.Preparation = kept
}

func saveProof(tx *sql.Tx, b *model.Bench, plan memberPlan, runID string) error {
	if runID != "" {
		r, err := history.Get(tx, runID)
		if err != nil {
			return err
		}
		if r.Key != b.Key || r.File == nil || r.ExitCode == nil || *r.ExitCode != 0 || !r.ReleasedAt.IsZero() {
			return errors.New("member preparation has no sealed successful outcome")
		}
		if plan.config != nil && plan.config.Hooks() {
			if err := setup.Record(tx, receipt(b, plan.member.Name), plan.digest, runID); err != nil {
				return err
			}
		}
		if err := retain(tx, b); err != nil {
			return err
		}
	}
	removeProof(b, plan.member.Name)
	b.Workspace.Preparation = append(b.Workspace.Preparation, model.WorkspacePreparation{Name: plan.member.Name, Digest: plan.digest, RunID: runID})
	sort.Slice(b.Workspace.Preparation, func(i, j int) bool { return b.Workspace.Preparation[i].Name < b.Workspace.Preparation[j].Name })
	return nil
}

func (m Manager) current(key string) (*model.Bench, error) {
	var b *model.Bench
	err := m.Read(func(rows benchstate.Database) error {
		b = workspace.Clone(rows[key])
		if b == nil || b.Workspace == nil || b.Path != filepath.Join(m.Data, "benches", key) {
			return errors.New("registered workspace at configured storage root required")
		}
		return nil
	})
	return b, err
}

// A completed request may race a subsequent acquisition before its final read.
// Its former token must never disclose the new owner's private capabilities.
func (m Manager) ownedCurrent(key, token string) (*model.Bench, error) {
	b, err := m.current(key)
	if err != nil {
		return nil, err
	}
	if b.Lease != nil {
		if _, err := owned(benchstate.Database{key: b}, key, token); err != nil {
			return nil, err
		}
	}
	return b, nil
}

// PrepareAcquired preserves the already-committed owner across cancellation
// before Prepare can enter its ownership boundary. The caller supplies only
// the private record returned by its successful acquisition or staging call.
func (m Manager) PrepareAcquired(ctx context.Context, acquired *model.Bench, selector string, offline bool) (*model.Bench, error) {
	if acquired == nil || acquired.Lease == nil {
		return nil, errors.New("newly acquired workspace owner required")
	}
	key, token := acquired.Key, acquired.Lease.Token
	result, err := m.Prepare(ctx, key, token, selector, offline)
	if result == nil {
		result = workspace.Clone(acquired)
	}
	if err == nil && selector == "" {
		verified, verifyErr := m.VerifyPrepared(ctx, key, token)
		if verified != nil {
			result = verified
		}
		err = verifyErr
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		stopped, stopErr := m.Stop(key, token, false)
		if stopped != nil {
			result = stopped
		}
		err = errors.Join(err, ctxErr, stopErr)
	}
	return result, err
}

// Prepare completes selected members under a held lease. An empty selector prepares
// every member, recording a resumable success proof after each sealed stage.
// The caller separately seals a checkpoint before publishing readiness.
func (m Manager) Prepare(ctx context.Context, key, token, selector string, offline bool) (result *model.Bench, operationErr error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, err := m.current(key)
	if err != nil {
		return nil, err
	}
	if _, err = owned(benchstate.Database{key: b}, key, token); err != nil {
		return nil, err
	}
	if err = m.preparable(b); err != nil {
		return b, err
	}
	if err = m.enterOperation(ctx, key, token); err != nil {
		return nil, err
	}
	defer m.reconcileCancellation(ctx, key, token, &result, &operationErr)
	if _, err = m.lifecycle().Inspect(ctx, b, false); err != nil {
		return b, err
	}
	var plans []memberPlan
	for _, member := range b.Workspace.Members {
		if selector != "" && selector != member.Name {
			continue
		}
		c, digest, e := freshtree.Inputs(ctx, m.Fresh, b, member)
		if e != nil {
			return b, e
		}
		if e = setup.MatchQuota(c, member.RequestedQuotaBytes); e != nil {
			return b, e
		}
		plans = append(plans, memberPlan{member: member, config: c, digest: digest})
	}
	if len(plans) == 0 {
		return b, errors.New("workspace member selector is not registered")
	}
	for _, plan := range plans {
		if err = m.member(ctx, key, token, plan, offline); err != nil {
			last, readErr := m.ownedCurrent(key, token)
			return last, errors.Join(err, readErr)
		}
	}
	err = m.Update(func(rows benchstate.Database, tx *sql.Tx) error {
		current, e := owned(rows, key, token)
		if e != nil {
			return e
		}
		if e = m.preparable(current); e != nil {
			return e
		}
		if err := m.transferAdmission(ctx, tx, current); err != nil {
			return err
		}
		if e = m.stopped(current); e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		for _, member := range current.Workspace.Members {
			_, digest, e := freshtree.Inputs(ctx, m.Fresh, current, member)
			if e != nil {
				return e
			}
			p := proof(current, member.Name)
			if p == nil || p.Digest != digest {
				return nil // Explicit single-member setup does not imply all-member preparation.
			}
			if e = verifyProof(tx, current, p); e != nil {
				return e
			}
		}
		if e = m.Prepared(current); e != nil {
			return e
		}
		if current.Workspace.Phase != "ready" {
			current.Workspace.Phase = "prepared"
		}
		return nil
	})
	last, readErr := m.ownedCurrent(key, token)
	return last, errors.Join(err, readErr)
}

func (m Manager) member(ctx context.Context, key, token string, plan memberPlan, offline bool) error {
	skip := false
	// Commit revocation before creating caches or running setup effects.
	if err := m.Update(func(rows benchstate.Database, tx *sql.Tx) error {
		b, err := owned(rows, key, token)
		if err != nil {
			return err
		}
		if err = m.preparable(b); err != nil {
			return err
		}
		if err := m.transferAdmission(ctx, tx, b); err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = m.stopped(b); err != nil {
			return err
		}
		if err = m.inspectMember(ctx, b, plan.member.Name); err != nil {
			return err
		}
		_, digest, err := freshtree.Inputs(ctx, m.Fresh, b, plan.member)
		if err != nil || digest != plan.digest {
			return errors.New("workspace preparation inputs changed before execution")
		}
		if p := proof(b, plan.member.Name); p != nil && p.Digest == digest {
			if err = verifyProof(tx, b, p); err != nil {
				return err
			}
			skip = true
			return nil
		}
		// A successful sealed execution can finish its proof after a failed save.
		if stage := b.Workspace.Stage; stage != nil && stage.Name == plan.member.Name && stage.Digest == digest && b.Lease.Execution != nil {
			r, e := history.Get(tx, b.Lease.Execution.ID)
			if e != nil {
				return e
			}
			if r.File != nil && r.ExitCode != nil && *r.ExitCode == 0 && r.ReleasedAt.IsZero() {
				if e = saveProof(tx, b, plan, r.ID); e != nil {
					return e
				}
				skip = true
				return nil
			}
		}
		if err = retain(tx, b); err != nil {
			return err
		}
		if b.Workspace.Phase != "ready" {
			b.Workspace.Phase = "initializing"
		}
		removeProof(b, plan.member.Name)
		if plan.config != nil && plan.config.Hooks() {
			done, e := setup.Completed(tx, receipt(b, plan.member.Name), plan.digest)
			if e != nil {
				return e
			}
			if !done {
				return setup.Invalidate(tx, receipt(b, plan.member.Name))
			}
		}
		return nil
	}); err != nil || skip {
		return err
	}
	return m.execute(ctx, key, token, plan, offline)
}

func (m Manager) environment(ctx context.Context, b *model.Bench, plan memberPlan) ([]string, error) {
	if fresh, err := freshtree.Selected(m.Fresh, b); err != nil || fresh {
		return freshtree.Environment(plan.config, err)
	}
	settings := plan.config.CacheSettings()
	if settings.Private() {
		tracked, err := m.Git(ctx, plan.member.Path, "ls-files", "--", ".bench/cache")
		if err != nil || tracked != "" {
			return nil, errors.New("checkout-local cache namespace must be untracked and Git-readable")
		}
	}
	paths, err := operationcontrol.Value(ctx, func() (cachepolicy.Paths, error) { return cachepolicy.Prepare(m.Data, plan.member.Path, settings) })
	if err != nil {
		return nil, err
	}
	return execution.EnvironmentFor(paths, os.Environ()), nil
}

// CheckPrepared is a read-only proof check inside the final lifecycle session.
func CheckPrepared(b *model.Bench) error { return Manager{}.Prepared(b) }

func (m Manager) checkPrepared(ctx context.Context, b *model.Bench) error {
	if b == nil || b.Workspace == nil || len(b.Workspace.Preparation) != len(b.Workspace.Members) {
		return errors.New("every workspace member requires preparation evidence")
	}
	for _, member := range b.Workspace.Members {
		c, digest, err := freshtree.Inputs(ctx, m.Fresh, b, member)
		if err != nil {
			return err
		}
		if err = setup.MatchQuota(c, member.RequestedQuotaBytes); err != nil {
			return err
		}
		p := proof(b, member.Name)
		if p == nil || p.Digest != digest {
			return errors.New("workspace preparation inputs changed; initialize again before readiness")
		}
		if p.RunID == "" {
			if c != nil && c.Hooks() {
				return errors.New("configured workspace setup requires successful execution evidence")
			}
			_, err := freshtree.FrozenCommand(ctx, m.Fresh, b, member, c, filepath.Dir(filepath.Dir(b.Path)), false)
			if !errors.Is(err, setup.ErrNoLockfile) {
				return errors.New("frozen workspace setup requires successful execution evidence")
			}
		}
	}
	return nil
}
