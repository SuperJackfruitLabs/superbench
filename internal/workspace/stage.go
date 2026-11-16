package workspace

import (
	"context"
	"database/sql"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/crosshost"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/handoffjournal"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statepath"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacecopy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceplan"
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
	Fresh       func(context.Context, *model.Bench) error
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

var ErrTransfer = errors.New("workspace ownership has a durable transfer; use its transfer commands")

var ErrInitializationRepair = errors.New("workspace initialization has an explicit repair epoch; use its repair commands")

func privateRecord(b *model.Bench) *model.Bench { return workspacecopy.Copy(b) }

// Clone owns all mutable workspace and lease slices returned to lifecycle callers.
func Clone(b *model.Bench) *model.Bench { return privateRecord(b) }

func matchesRequest(b *model.Bench, creationID string, spec Spec, data string) bool {
	if b == nil || b.Workspace == nil || b.Workspace.RecoveredFrom != nil || workspacepolicy.Validate(b) != nil {
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
	if spec.TransferID != "" {
		return m.stageTransferred(ctx, creationID, spec)
	}
	if err := ValidateStage(creationID, spec); err != nil {
		return nil, err
	}
	spec, _ = normalize(spec)
	return m.stage(ctx, stagingRequest{
		key: spec.Key, creationID: creationID, explicitQuota: spec.QuotaBytes,
		matches: func(b *model.Bench) bool { return matchesRequest(b, creationID, spec, m.Data) },
		selectPlan: func(ctx context.Context, timeout time.Duration) (Plan, error) {
			return (Planner{Mirrors: repository.Mirrors{State: m.State, Data: m.Data, Timeout: timeout}, Git: m.Git}).Select(ctx, spec)
		},
	})
}

// Both fresh creation and recovery enter the same durable ownership/allocation
// sequence; only source selection and exact retry identity differ.
type stagingRequest struct {
	validate        func(benchstate.Database, *sql.Tx) error
	key, creationID string
	transferID      string
	identity        func() string
	crossHost       *crosshost.Artifact
	transferredFrom *model.WorkspaceTransferOrigin
	explicitQuota   uint64
	recoveredFrom   *model.WorkspaceRecovery
	matches         func(*model.Bench) bool
	selectPlan      func(context.Context, time.Duration) (Plan, error)
}

func (m Manager) stage(ctx context.Context, request stagingRequest) (*model.Bench, error) {
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
		if err := update(func(rows benchstate.Database, tx *sql.Tx) error {
			if request.crossHost == nil {
				if err := handoffjournal.Unreserved(tx, request.key); err != nil {
					return err
				}
			}
			if request.validate != nil {
				if err := request.validate(rows, tx); err != nil {
					return err
				}
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if b, ok := rows[request.key]; ok {
				if b.Workspace != nil && (b.Workspace.Transfer != nil || b.Workspace.TransferredFrom != nil && request.transferID != b.Workspace.TransferredFrom.ID) {
					return ErrTransfer
				}
				if b.Workspace != nil && b.Workspace.InitializationRepair != nil {
					return ErrInitializationRepair
				}
				if !request.matches(b) {
					return errors.New("workspace creation conflicts with an existing bench")
				}
				if err := m.stageTransferCheck(ctx, tx, b); err != nil {
					return err
				}
				found = privateRecord(b)
			}
			bid := ""
			if found != nil {
				bid = found.Workspace.ID
			}
			if err := operationcontrol.AdmitContext(ctx, tx, request.key, bid); err != nil {
				return err
			}
			if found != nil && found.Lease != nil && found.Workspace.Phase != "ready" {
				return operationcontrol.BindContext(ctx, tx, found)
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
			for _, path := range []string{filepath.Join(m.State, "create-"+request.key+".json"), filepath.Join(m.Data, "benches", request.key)} {
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
			plan, err := request.selectPlan(ctx, timeout)
			if err != nil {
				return err
			}
			if _, err := validateMaterialization(plan); err != nil {
				return err
			}
			candidate, err := recordPlan(plan, request.creationID, request.explicitQuota)
			if err != nil {
				return err
			}
			if request.transferredFrom != nil {
				origin := *request.transferredFrom
				candidate.Workspace.TransferredFrom = &origin
				candidate.Workspace.ID = request.identity()
			}
			if request.recoveredFrom != nil {
				origin := *request.recoveredFrom
				candidate.Workspace.RecoveredFrom = &origin
				if err := workspacepolicy.Validate(candidate); err != nil {
					return err
				}
			}
			if err = update(func(rows benchstate.Database, tx *sql.Tx) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if _, ok := rows[request.key]; ok {
					return errors.New("workspace identity appeared during source selection")
				}
				if err := m.stageTransferCheck(ctx, tx, candidate); err != nil {
					return err
				}
				if err := operationcontrol.BindContext(ctx, tx, candidate); err != nil {
					return err
				}
				rows[request.key] = privateRecord(candidate)
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
		if committed.Workspace.TransferredFrom != nil {
			if err := update(func(_ benchstate.Database, tx *sql.Tx) error {
				return m.stageTransferCheck(ctx, tx, committed)
			}); err != nil {
				return err
			}
		}
		if m.Fresh != nil {
			// A durable assignment follows a validated completed bootstrap. Retry
			// the same pinned owner/vector through the gate without allocating again.
			// An effect without a committed assignment still needs reconciliation.
			if committed.ProjectID == 0 {
				if err := m.Fresh(ctx, allocated); err != nil {
					return err
				}
				if allocated.ProjectID < 10000 || allocated.QuotaBytes == 0 || allocated.QuotaInodes == 0 {
					return errors.New("fresh workspace returned no verified allocation")
				}
			}
		} else if err := m.Allocate(allocated); err != nil {
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
			return update(func(rows benchstate.Database, tx *sql.Tx) error {
				if err := gateCtx.Err(); err != nil {
					return err
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				if !reflect.DeepEqual(rows[request.key], committed) || !reflect.DeepEqual(workspaceplan.FromRecord(committed), plan) {
					return errors.New("workspace initialization ownership or pinned vector changed")
				}
				return m.stageTransferCheck(gateCtx, tx, rows[request.key])
			})
		}
		if err := update(func(rows benchstate.Database, tx *sql.Tx) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !reflect.DeepEqual(rows[request.key], committed) {
				return errors.New("workspace initialization ownership changed")
			}
			if err := m.stageTransferCheck(ctx, tx, allocated); err != nil {
				return err
			}
			rows[request.key] = privateRecord(allocated)
			return nil
		}); err != nil {
			return err
		}
		committed = privateRecord(allocated)
		plan := workspaceplan.FromRecord(committed)
		if m.Fresh == nil {
			if err := (Materializer{Git: m.Git, Gate: gate, Timeout: timeout}).Materialize(ctx, plan); err != nil {
				return err
			}
		} else if err := gate(ctx, plan); err != nil {
			return err
		}
		finished := privateRecord(committed)
		finished.Workspace.Phase = "initializing"
		if err := update(func(rows benchstate.Database, tx *sql.Tx) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !reflect.DeepEqual(rows[request.key], committed) {
				return errors.New("workspace initialization ownership changed")
			}
			if err := m.stageTransferCheck(ctx, tx, finished); err != nil {
				return err
			}
			rows[request.key] = privateRecord(finished)
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
