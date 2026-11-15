package workspace

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statepath"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
)

// Update commits one durable transition while the enclosing Within retains the
// host-wide lock. An error must roll back this update, not earlier commits.
type Change = func(benchstate.Database, *sql.Tx) error

type Update = func(Change) error

type Manager struct {
	State, Data string
	Timeout     time.Duration
	Within      func(func(Update) error) error
	Git         func(context.Context, string, ...string) (string, error)
	Admit       func() error
	Allocate    func(*model.Bench) error
}

// ValidateStage checks request shape without filesystem or repository effects.
func ValidateStage(creationID string, spec Spec) error {
	if !keyPattern.MatchString(creationID) {
		return errors.New("invalid workspace creation request identity")
	}
	normalized, err := normalize(spec)
	if err != nil {
		return err
	}
	for i, member := range normalized.Repositories {
		for _, previous := range normalized.Repositories[:i] {
			if previous.Repository == member.Repository && branchNamespaceConflict(previous.Branch, member.Branch) {
				return errors.New("workspace task branches conflict in the repository ref namespace")
			}
		}
	}
	return nil
}

func privateRecord(b *model.Bench) *model.Bench {
	if b == nil {
		return nil
	}
	c := *b
	if b.Workspace != nil {
		w := *b.Workspace
		w.Members = append([]model.WorkspaceMember(nil), w.Members...)
		w.Preparation = append([]model.WorkspacePreparation(nil), w.Preparation...)
		cloneCheckpoint := func(cp *model.WorkspaceCheckpoint) *model.WorkspaceCheckpoint {
			if cp == nil {
				return nil
			}
			copy := *cp
			copy.Members = append([]model.WorkspaceCommit(nil), cp.Members...)
			return &copy
		}
		w.Pending, w.Checkpoint = cloneCheckpoint(w.Pending), cloneCheckpoint(w.Checkpoint)
		if w.Stage != nil {
			stage := *w.Stage
			w.Stage = &stage
		}
		c.Workspace = &w
	}
	if b.Lease != nil {
		l := *b.Lease
		l.RetainedRuns = append([]string(nil), l.RetainedRuns...)
		if l.Execution != nil {
			e := *l.Execution
			l.Execution = &e
		}
		c.Lease = &l
	}
	return &c
}

// Clone owns all mutable workspace and lease slices returned to lifecycle callers.
func Clone(b *model.Bench) *model.Bench { return privateRecord(b) }

func stagedPlan(b *model.Bench) Plan {
	p := Plan{Key: b.Key, Root: b.Path, QuotaBytes: b.RequestedQuotaBytes, CheckpointRepository: b.Workspace.CheckpointRepository}
	for _, v := range b.Workspace.Members {
		p.Members = append(p.Members, Member{Name: v.Name, Repository: v.Repository, Branch: v.Branch, Base: v.Base, Path: v.Path, Mirror: v.Mirror, Source: v.Source, ObjectFormat: v.ObjectFormat, RepositoryQuotaBytes: v.RequestedQuotaBytes})
	}
	return p
}

func matchesRequest(b *model.Bench, creationID string, spec Spec, data string) bool {
	if b == nil || b.Workspace == nil || workspacepolicy.Validate(b) != nil {
		return false
	}
	w := b.Workspace
	if b.Key != spec.Key || b.Path != filepath.Join(data, "benches", spec.Key) || w.CreationID != creationID || w.CheckpointRepository != spec.CheckpointRepository || w.ExplicitQuotaBytes != spec.QuotaBytes || len(w.Members) != len(spec.Repositories) {
		return false
	}
	for i, v := range w.Members {
		r := spec.Repositories[i]
		if v.Name != r.Name || v.Repository != r.Repository || v.Branch != r.Branch || v.Base != r.Base {
			return false
		}
	}
	return true
}

func recordPlan(p Plan, creationID string, explicit uint64) (*model.Bench, error) {
	id, err := model.Nonce()
	if err != nil {
		return nil, err
	}
	lease, err := model.NewLease("initialization")
	if err != nil {
		return nil, err
	}
	lease.Continuation, err = model.Nonce()
	if err != nil {
		return nil, err
	}
	b := &model.Bench{Key: p.Key, Path: p.Root, RequestedQuotaBytes: p.QuotaBytes, Lease: lease, Workspace: &model.Workspace{ID: id, CreationID: creationID, CheckpointRepository: p.CheckpointRepository, Phase: "materializing", ExplicitQuotaBytes: explicit}}
	for _, v := range p.Members {
		b.Workspace.Members = append(b.Workspace.Members, model.WorkspaceMember{Name: v.Name, Repository: v.Repository, Branch: v.Branch, Base: v.Base, Path: v.Path, Mirror: v.Mirror, Source: v.Source, ObjectFormat: v.ObjectFormat, RequestedQuotaBytes: v.RepositoryQuotaBytes})
	}
	return b, workspacepolicy.Validate(b)
}

// Stage persists the complete source vector and its initialization owner before
// quota or checkout effects. Errors return the last committed private record.
func (m Manager) Stage(ctx context.Context, creationID string, spec Spec) (*model.Bench, error) {
	if err := ValidateStage(creationID, spec); err != nil {
		return nil, err
	}
	spec, _ = normalize(spec)
	timeout := m.Timeout
	if timeout == 0 {
		timeout = repository.DefaultTimeout
	}
	if timeout < 0 || timeout > time.Hour {
		return nil, errors.New("invalid workspace staging timeout")
	}
	if m.Within == nil || m.Git == nil || m.Admit == nil || m.Allocate == nil {
		return nil, errors.New("workspace staging requires storage, managed Git, admission and allocation")
	}
	if err := statepath.Validate(m.State, m.Data); err != nil {
		return nil, err
	}
	if filepath.Clean(m.State) != m.State || filepath.Clean(m.Data) != m.Data {
		return nil, errors.New("workspace storage paths must be normalized")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var committed *model.Bench
	err := m.Within(func(update Update) error {
		// Even an idempotent retry first enters migration/lookup under the host lock.
		var found *model.Bench
		if err := update(func(rows benchstate.Database, _ *sql.Tx) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if b, ok := rows[spec.Key]; ok {
				if !matchesRequest(b, creationID, spec, m.Data) {
					return errors.New("workspace creation conflicts with an existing bench")
				}
				found = privateRecord(b)
			}
			return nil
		}); err != nil {
			return err
		}
		committed = found
		if found != nil && found.Workspace.Phase != "materializing" {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if committed == nil {
			for _, path := range []string{filepath.Join(m.State, "create-"+spec.Key+".json"), filepath.Join(m.Data, "benches", spec.Key)} {
				if _, err := os.Lstat(path); err == nil {
					return errors.New("unregistered workspace files or legacy creation intent exist; preserve and reconcile")
				} else if !os.IsNotExist(err) {
					return err
				}
			}
			if err := physicalDirectory(m.Data, true); err != nil {
				return err
			}
			if err := physicalDirectory(filepath.Join(m.Data, "benches"), true); err != nil {
				return err
			}
			if err := m.Admit(); err != nil {
				return err
			}
			planner := Planner{Mirrors: repository.Mirrors{State: m.State, Data: m.Data, Timeout: timeout}, Git: m.Git}
			plan, err := planner.Select(ctx, spec)
			if err != nil {
				return err
			}
			if _, err := validateMaterialization(plan); err != nil {
				return err
			}
			candidate, err := recordPlan(plan, creationID, spec.QuotaBytes)
			if err != nil {
				return err
			}
			if err = update(func(rows benchstate.Database, _ *sql.Tx) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if _, ok := rows[spec.Key]; ok {
					return errors.New("workspace identity appeared during source selection")
				}
				rows[spec.Key] = privateRecord(candidate)
				return nil
			}); err != nil {
				return err
			}
			committed = privateRecord(candidate)
		} else if err := m.Admit(); err != nil {
			return err
		}
		// Allocate is idempotent by logical key. Its ledger can survive a failed SQL
		// save; a retry retrieves that assignment before any checkout is created.
		allocated := privateRecord(committed)
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := m.Allocate(allocated); err != nil {
			return err
		}
		if committed.ProjectID != 0 && (allocated.ProjectID != committed.ProjectID || allocated.QuotaBytes != committed.QuotaBytes || allocated.QuotaInodes != committed.QuotaInodes) {
			return errors.New("workspace allocator changed an assigned quota")
		}
		identity := privateRecord(allocated)
		identity.ProjectID = committed.ProjectID
		identity.QuotaBytes = committed.QuotaBytes
		identity.QuotaInodes = committed.QuotaInodes
		if !reflect.DeepEqual(identity, committed) {
			return errors.New("workspace allocator changed non-allocation metadata")
		}
		if err := workspacepolicy.Validate(allocated); err != nil {
			return err
		}
		gate := func(gateCtx context.Context, plan Plan) error {
			if err := gateCtx.Err(); err != nil {
				return err
			}
			return update(func(rows benchstate.Database, _ *sql.Tx) error {
				if err := gateCtx.Err(); err != nil {
					return err
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				if !reflect.DeepEqual(rows[spec.Key], committed) || !reflect.DeepEqual(stagedPlan(committed), plan) {
					return errors.New("workspace initialization ownership or pinned vector changed")
				}
				return nil
			})
		}
		if err := update(func(rows benchstate.Database, _ *sql.Tx) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !reflect.DeepEqual(rows[spec.Key], committed) {
				return errors.New("workspace initialization ownership changed")
			}
			rows[spec.Key] = privateRecord(allocated)
			return nil
		}); err != nil {
			return err
		}
		committed = privateRecord(allocated)
		plan := stagedPlan(committed)
		if err := (Materializer{Git: m.Git, Gate: gate, Timeout: timeout}).Materialize(ctx, plan); err != nil {
			return err
		}
		finished := privateRecord(committed)
		finished.Workspace.Phase = "initializing"
		if err := update(func(rows benchstate.Database, _ *sql.Tx) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !reflect.DeepEqual(rows[spec.Key], committed) {
				return errors.New("workspace initialization ownership changed")
			}
			rows[spec.Key] = privateRecord(finished)
			return nil
		}); err != nil {
			return err
		}
		committed = privateRecord(finished)
		return nil
	})
	result := privateRecord(committed)
	// Creation identity authorizes its initialization retry, not a later writer
	// grant. Completed-creation replay exposes inventory without capabilities.
	if result != nil && result.Workspace.Phase == "ready" && result.Lease != nil {
		result.Lease.Token = ""
		result.Lease.Continuation = ""
	}
	return result, err
}
