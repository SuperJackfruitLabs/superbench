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
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
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

func verifyProof(tx *sql.Tx, b *model.Bench, p *model.WorkspacePreparation) error {
	if p.RunID == "" {
		return nil
	}
	owned := false
	for _, id := range b.Lease.RetainedRuns {
		owned = owned || id == p.RunID
	}
	if e := b.Lease.Execution; e != nil && e.Phase == "stopped" && e.ID == p.RunID {
		owned = true
	}
	r, err := history.Get(tx, p.RunID)
	if err != nil {
		return err
	}
	if !owned || r.Key != b.Key || r.File == nil || r.ExitCode == nil || *r.ExitCode != 0 || !r.ReleasedAt.IsZero() {
		return errors.New("member preparation proof requires an owned sealed successful run")
	}
	return nil
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

// Prepare completes selected initialization members. An empty selector prepares
// every member, recording a resumable success proof after each sealed stage.
// The caller separately seals a checkpoint before publishing readiness.
func (m Manager) Prepare(ctx context.Context, key, token, selector string, offline bool) (*model.Bench, error) {
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
		return b, err
	}
	if err = initializing(b); err != nil {
		return b, err
	}
	if _, err = (workspace.Lifecycle{State: m.State, Data: m.Data, Git: m.Git}).Inspect(ctx, b, false); err != nil {
		return b, err
	}
	var plans []memberPlan
	for _, member := range b.Workspace.Members {
		if selector != "" && selector != member.Name {
			continue
		}
		c, digest, e := inputs(member.Path)
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
			last, readErr := m.current(key)
			return last, errors.Join(err, readErr)
		}
	}
	err = m.Update(func(rows benchstate.Database, tx *sql.Tx) error {
		current, e := owned(rows, key, token)
		if e != nil {
			return e
		}
		if e = initializing(current); e != nil {
			return e
		}
		if e = m.stopped(current); e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		for _, member := range current.Workspace.Members {
			_, digest, e := inputs(member.Path)
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
		if e = CheckPrepared(current); e != nil {
			return e
		}
		current.Workspace.Phase = "prepared"
		return nil
	})
	last, readErr := m.current(key)
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
		if err = initializing(b); err != nil {
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
		_, digest, err := inputs(plan.member.Path)
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
		b.Workspace.Phase = "initializing"
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

func (m Manager) environment(ctx context.Context, plan memberPlan) ([]string, error) {
	settings := plan.config.CacheSettings()
	if settings.Private() {
		tracked, err := m.Git(ctx, plan.member.Path, "ls-files", "--", ".bench/cache")
		if err != nil || tracked != "" {
			return nil, errors.New("checkout-local cache namespace must be untracked and Git-readable")
		}
	}
	paths, err := cachepolicy.Prepare(m.Data, plan.member.Path, settings)
	if err != nil {
		return nil, err
	}
	return execution.EnvironmentFor(paths, os.Environ()), nil
}

// CheckPrepared is a read-only proof check inside the final lifecycle session.
func CheckPrepared(b *model.Bench) error {
	if b == nil || b.Workspace == nil || len(b.Workspace.Preparation) != len(b.Workspace.Members) {
		return errors.New("every workspace member requires preparation evidence")
	}
	for _, member := range b.Workspace.Members {
		c, digest, err := inputs(member.Path)
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
			_, err := setup.FrozenCommandFor(member.Path, cachepolicy.Select(filepath.Dir(filepath.Dir(b.Path)), member.Path, c.CacheSettings()), false)
			if !errors.Is(err, setup.ErrNoLockfile) {
				return errors.New("frozen workspace setup requires successful execution evidence")
			}
		}
	}
	return nil
}
