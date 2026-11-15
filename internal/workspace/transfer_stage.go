package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reftransfer"
)

// stageTransferred consumes an immutable local approval after the source has
// permanently relinquished its branches. Merely knowing a creation ID does not
// authorize this path; both the source tombstone and transfer journal must agree.
func (m Manager) stageTransferred(ctx context.Context, creationID string, spec Spec) (*model.Bench, error) {
	if !keyPattern.MatchString(spec.TransferID) || !keyPattern.MatchString(creationID) || !keyPattern.MatchString(spec.Key) {
		return nil, errors.New("invalid workspace transfer staging identity")
	}
	var recovery RecoverySpec
	var targetID string
	var bases []string
	origin := &model.WorkspaceTransferOrigin{}
	source := &model.WorkspaceRecovery{}
	request := stagingRequest{key: spec.Key, creationID: creationID, transferID: spec.TransferID, explicitQuota: spec.QuotaBytes, transferredFrom: origin, recoveredFrom: source}
	request.validate = func(rows benchstate.Database, tx *sql.Tx) error {
		j, err := reftransfer.Get(tx, spec.TransferID)
		if err != nil {
			return err
		}
		var approval struct {
			Root, DataRoot, ID, SourceKey, SourceID, TargetKey, TargetID, CreationID, Digest, CheckpointRepository string
			QuotaBytes                                                                                             uint64
			Repositories                                                                                           []Repository
		}
		if err = json.Unmarshal(j.Plan, &approval); err != nil {
			return err
		}
		if approval.Root != m.State || approval.DataRoot != m.Data || approval.ID != j.ID || approval.SourceKey != j.SourceKey || approval.SourceID != j.SourceID || approval.TargetKey != j.TargetKey || approval.TargetID != j.TargetID || approval.CreationID != j.CreationID || approval.Digest != j.Digest || approval.QuotaBytes != spec.QuotaBytes || approval.CheckpointRepository != spec.CheckpointRepository || !reflect.DeepEqual(approval.Repositories, spec.Repositories) {
			return errors.New("destination selection differs from the reviewed transfer approval")
		}
		b := rows[j.SourceKey]
		if b == nil || b.Workspace == nil || b.Workspace.ID != j.SourceID || b.Workspace.Transfer == nil || b.Lease != nil || b.Workspace.Checkpoint == nil {
			return errors.New("workspace transfer source is not permanently relinquished")
		}
		t := b.Workspace.Transfer
		if (j.Phase != "relinquished" && j.Phase != "completed") || t.Phase != "transferred" || t.ID != j.ID || t.Digest != j.Digest || t.TargetID != j.TargetID || t.TargetKey != j.TargetKey || t.CreationID != j.CreationID || j.TargetKey != spec.Key || j.CreationID != creationID || spec.CheckpointRepository != b.Workspace.CheckpointRepository {
			return errors.New("workspace transfer journal and destination do not agree")
		}
		var repository string
		for _, member := range b.Workspace.Members {
			if member.Name == b.Workspace.CheckpointRepository {
				repository = member.Repository
			}
		}
		*source = model.WorkspaceRecovery{Repository: repository, BenchID: b.Workspace.ID, Commit: b.Workspace.Checkpoint.Seal, ManifestSHA256: b.Workspace.Checkpoint.Digest}
		recovery, err = recoveryRequest(creationID, RecoverySpec{Key: spec.Key, Repositories: spec.Repositories, Source: *source, QuotaBytes: spec.QuotaBytes})
		if err != nil {
			return err
		}
		if len(recovery.Repositories) != len(b.Workspace.Members) {
			return errors.New("workspace transfer must retain the exact source allowlist")
		}
		bases = nil
		for i, member := range b.Workspace.Members {
			bases = append(bases, member.Base)
			r := recovery.Repositories[i]
			if r.Name != member.Name || r.Repository != member.Repository || r.Branch != member.Branch {
				return errors.New("workspace transfer must retain the original task refs")
			}
		}
		*origin = model.WorkspaceTransferOrigin{ID: j.ID, Digest: j.Digest, SourceKey: j.SourceKey, SourceID: j.SourceID}
		targetID = j.TargetID
		return nil
	}
	request.matches = func(b *model.Bench) bool {
		return matchesRecovery(b, creationID, recovery, m.Data) && b.Workspace.ID == targetID && reflect.DeepEqual(b.Workspace.TransferredFrom, origin)
	}
	request.selectPlan = func(ctx context.Context, timeout time.Duration) (Plan, error) {
		plan, err := m.recoveryPlanAuthorized(ctx, timeout, recovery, true)
		if err == nil {
			for i := range plan.Members {
				plan.Members[i].Base = bases[i]
			}
		}
		return plan, err
	}
	// The closures populate the target identity during the first guarded lookup.
	// stage receives its own value copy, so carry identity through the shared origin
	// validation rather than letting a caller choose a new workspace ID on retry.
	request.identity = func() string { return targetID }
	return m.stage(ctx, request)
}
