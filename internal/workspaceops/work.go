package workspaceops

import (
	"context"
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

func (m Manager) create(ctx context.Context, r protocolv2.Request, uid uint32) (*Result, error) {
	id, err := CreationID(r, uid)
	if err != nil {
		return nil, err
	}
	spec := creationSpec(r)
	if rows, readErr := m.Store.Snapshot(r.Key); readErr == nil {
		if rows[0].Workspace == nil || rows[0].Workspace.CreationID != id {
			return nil, ErrConflict
		}
	} else if !errors.Is(readErr, bench.ErrNotFound) {
		return nil, readErr
	}
	var b *model.Bench
	if r.Operation == "create" {
		b, err = m.Store.InitializeWorkspaceContext(ctx, id, spec)
	} else {
		if r.Source == nil {
			return nil, ErrInvalid
		}
		b, err = m.Store.RecoverWorkspaceContext(ctx, id, recoverySpec(r, spec))
	}
	if b == nil {
		return nil, err
	}
	if b.Workspace == nil || b.Workspace.CreationID != id {
		return nil, errors.Join(err, ErrConflict)
	}
	grant := ""
	root := b.Workspace.Phase == "ready" && b.Lease == nil
	if b.Lease != nil && b.Lease.Holder == "initialization" && (b.Workspace.Phase == "materializing" || b.Workspace.Phase == "initializing" || b.Workspace.Phase == "prepared") {
		grant, root = b.Lease.Token, true
	}
	return result(b, grant, root), err
}

func creationSpec(r protocolv2.Request) workspace.Spec {
	spec := workspace.Spec{Key: r.Key, CheckpointRepository: r.CheckpointRepository}
	if r.QuotaBytes != nil {
		spec.QuotaBytes = *r.QuotaBytes
	}
	for _, member := range r.Repositories {
		spec.Repositories = append(spec.Repositories, workspace.Repository{Name: member.Name, Repository: member.Repository, Branch: member.Branch, Base: member.Base})
	}
	return spec
}

func recoverySpec(r protocolv2.Request, spec workspace.Spec) workspace.RecoverySpec {
	s := r.Source
	return workspace.RecoverySpec{Key: r.Key, Repositories: spec.Repositories, QuotaBytes: spec.QuotaBytes, Source: workspace.RecoverySource{Repository: s.Repository, BenchID: s.BenchID, Commit: s.Commit, ManifestSHA256: s.ManifestSHA256}}
}

func (m Manager) work(ctx context.Context, r protocolv2.Request) (out *Result, operationErr error) {
	token, grant := r.LeaseToken, ""
	var b *model.Bench
	var err error
	if token != "" {
		if err := m.Store.CheckWorkspaceOwner(r.Key, token, r.Holder); err != nil {
			return nil, err
		}
	} else {
		b, err = m.Store.AcquireWorkspaceOwnerContext(ctx, r.Key, r.Holder)
		if b != nil && b.Lease != nil && b.Lease.Holder == r.Holder {
			token, grant = b.Lease.Token, b.Lease.Token
		}
		if err != nil {
			return result(b, grant, grant != ""), err
		}
		if token == "" {
			return nil, ErrUnavailable
		}
	}
	defer m.reconcileCanceledWork(ctx, r, token, grant, &out, &operationErr)
	if r.Operation == "run" {
		run, runErr := m.Store.RunWorkspaceContext(ctx, r.Key, token, r.RepositoryName, r.Arguments, false)
		if run == nil {
			return result(b, grant, grant != ""), runErr
		}
		b, err = run.Bench, runErr
		out := result(b, grant, true)
		if out == nil {
			return nil, err
		}
		out.Execution = run.Execution
		if run.ExitCode >= 0 {
			exit, truncated := run.ExitCode, run.LogTruncated
			out.ExitCode, out.LogTruncated = &exit, &truncated
		}
		if err == nil && run.ExitCode == 0 && (r.Checkpoint == nil || *r.Checkpoint) {
			b, err = m.complete(ctx, r.Key, token)
			finished := result(b, grant, true)
			if finished != nil {
				finished.Execution, finished.ExitCode, finished.LogTruncated = out.Execution, out.ExitCode, out.LogTruncated
				out = finished
			}
		}
		return out, err
	}
	prepared, prepareErr := m.Store.PrepareWorkspaceContext(ctx, r.Key, token, r.RepositoryName, r.Offline != nil && *r.Offline)
	if prepared != nil {
		b = prepared
	} else if m.Store.CheckWorkspaceOwner(r.Key, token, r.Holder) != nil {
		b = nil
	}
	err = prepareErr
	if err == nil && r.Operation == "setup" {
		if b.Workspace.Phase != "ready" {
			if b.Workspace.Phase != "prepared" {
				err = ErrConflict
			} else {
				finished, finishErr := m.Store.FinishWorkspaceInitializationContext(ctx, r.Key, token)
				if finished != nil {
					b = finished
				}
				err = finishErr
			}
		} else {
			b, err = m.Store.CompleteWorkspaceSetupContext(ctx, r.Key, token)
		}
	}
	if err == nil && r.Operation == "lease" {
		b, err = m.Store.VerifyWorkspacePreparationContext(ctx, r.Key, token)
	}
	if errors.Is(err, bench.ErrWorkspacePreparation) {
		err = errors.Join(ErrConflict, err)
	}
	out = result(b, grant, true)
	selectPreparation(out, r)
	return out, err
}

func selectPreparation(out *Result, r protocolv2.Request) {
	if out == nil || r.Operation != "setup" || r.RepositoryName == "" {
		return
	}
	var selected []Preparation
	for _, p := range out.Preparation {
		if p.Name == r.RepositoryName {
			selected = append(selected, p)
		}
	}
	out.Preparation = selected
}

func (m Manager) complete(ctx context.Context, key, token string) (*model.Bench, error) {
	b, err := m.Store.CheckpointWorkspaceContext(ctx, key, token)
	if err != nil {
		return b, err
	}
	released, err := m.Store.ReleaseWorkspaceContext(ctx, key, token)
	if released != nil {
		b = released
	}
	return b, err
}

// Preserve service-visible sentinels without exposing backend error strings.
func KnownError(err error) bool {
	for _, known := range []error{ErrInvalid, ErrConflict, ErrUnavailable, bench.ErrNotFound, bench.ErrLease, bench.ErrBusy, bench.ErrScalar, bench.ErrExists, bench.ErrDirty, bench.ErrCheckpoint, bench.ErrNotFenced, bench.ErrArchived} {
		if errors.Is(err, known) {
			return true
		}
	}
	return false
}
