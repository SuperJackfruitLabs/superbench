package workspace

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/crosshost"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/handoffjournal"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hostidentity"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

// StageHandoff consumes only the exact locally accepted import. It cannot
// authorize a source export: independent reviewed source retirement precedes it.
func (m Manager) StageHandoff(ctx context.Context, artifact crosshost.Artifact) (*model.Bench, error) {
	if err := artifact.RequireDestination(artifact.Destination); err != nil {
		return nil, err
	}
	if m.Within == nil {
		return nil, errors.New("handoff staging requires state authority")
	}
	d := artifact.Destination
	var sourceRepo string
	spec := RecoverySpec{Key: d.Key}
	for _, r := range artifact.Members {
		spec.Repositories = append(spec.Repositories, Repository{Name: r.Name, Repository: r.Repository, Branch: r.Branch})
		if r.Name == artifact.CheckpointRepository {
			sourceRepo = r.Repository
		}
	}
	spec.Source = RecoverySource{Repository: sourceRepo, BenchID: artifact.Source.WorkspaceID, Commit: artifact.Seal, ManifestSHA256: artifact.ManifestDigest}
	origin := &model.WorkspaceTransferOrigin{ID: artifact.Source.TransferID, Digest: artifact.Source.ApprovalDigest, SourceKey: artifact.Source.Key, SourceID: artifact.Source.WorkspaceID, CrossHost: &model.WorkspaceHandoff{HostID: artifact.Source.HostID, StateID: artifact.Source.StateID, ArtifactDigest: artifact.Digest}}
	request := stagingRequest{key: d.Key, creationID: d.CreationID, transferID: origin.ID, crossHost: &artifact, identity: func() string { return d.WorkspaceID }, transferredFrom: origin, recoveredFrom: &spec.Source}
	request.validate = func(_ benchstate.Database, tx *sql.Tx) error {
		i, err := handoffjournal.ImportByID(tx, d.CreationID)
		if err != nil || !reflect.DeepEqual(i.Artifact, artifact) {
			return errors.New("import staging differs from retained artifact")
		}
		o, err := handoffjournal.OfferByID(tx, d.CreationID)
		if err != nil {
			return err
		}
		id, err := hostidentity.Current(ctx)
		if err != nil {
			return err
		}
		if id != d.HostID {
			return errors.New("import offer belongs to another actual host")
		}
		spec.QuotaBytes = o.QuotaBytes
		request.explicitQuota = o.QuotaBytes
		return ctx.Err()
	}
	// stage takes a value copy. Read the offer before constructing that copy;
	// request.validate independently rechecks it under every retained host session.
	if err := m.Within(func(update Update) error { return update(request.validate) }); err != nil {
		return nil, err
	}
	request.matches = func(b *model.Bench) bool {
		return matchesRecovery(b, d.CreationID, spec, m.Data) && b.Workspace.ID == d.WorkspaceID && reflect.DeepEqual(b.Workspace.TransferredFrom, origin)
	}
	request.selectPlan = func(ctx context.Context, timeout time.Duration) (Plan, error) {
		plan, err := m.recoveryPlanAuthorized(ctx, timeout, spec, true)
		if err != nil {
			return plan, err
		}
		if plan.CheckpointRepository != artifact.CheckpointRepository || len(plan.Members) != len(artifact.Members) {
			return Plan{}, errors.New("import seal differs from artifact vector")
		}
		for n, r := range artifact.Members {
			if plan.Members[n].Source != r.Commit || plan.Members[n].Branch != r.Branch {
				return Plan{}, errors.New("import source differs from approved original refs")
			}
			plan.Members[n].Base = r.IntegrationBase
		}
		return plan, nil
	}
	return m.stage(ctx, request)
}
