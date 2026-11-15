package workspace

import (
	"context"
	"encoding/hex"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/checkpointset"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/setup"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
)

type RecoverySource = model.WorkspaceRecovery

// RecoverySpec selects one exact historical seal and explicitly authorizes its
// complete repository set. Branches name new task refs; Base is forbidden.
type RecoverySpec struct {
	Key          string
	Repositories []Repository
	Source       RecoverySource
	QuotaBytes   uint64
}

func recoveryHex(s string, width int) bool {
	if len(s) != width || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func recoveryRequest(creationID string, spec RecoverySpec) (RecoverySpec, error) {
	invalid := errors.New("invalid exact workspace recovery request")
	if len(spec.Repositories) == 0 {
		return RecoverySpec{}, invalid
	}
	for _, member := range spec.Repositories {
		if member.Base != "" {
			return RecoverySpec{}, invalid
		}
	}
	// Reuse the same member/branch/quota grammar as creation. This temporary
	// coordinator validates shape only; recovery derives its real coordinator
	// exclusively from the sealed manifest after validating the exact allowlist.
	shape := Spec{Key: spec.Key, Repositories: spec.Repositories, CheckpointRepository: spec.Repositories[0].Name, QuotaBytes: spec.QuotaBytes}
	if err := ValidateStage(creationID, shape); err != nil {
		return RecoverySpec{}, err
	}
	normalized, _ := normalize(shape)
	spec.Repositories = normalized.Repositories
	found := false
	for i := range spec.Repositories {
		spec.Repositories[i].Base = ""
		found = found || spec.Repositories[i].Repository == spec.Source.Repository
	}
	source := spec.Source
	if !found || !recoveryHex(source.BenchID, 32) || !model.ValidCommit(source.Commit) || strings.ToLower(source.Commit) != source.Commit || !recoveryHex(source.ManifestSHA256, 64) {
		return RecoverySpec{}, invalid
	}
	return spec, nil
}

// ValidateRecovery is pure and precedes state, quota, mirror and checkout effects.
func ValidateRecovery(creationID string, spec RecoverySpec) error {
	_, err := recoveryRequest(creationID, spec)
	return err
}

func matchesRecovery(b *model.Bench, creationID string, spec RecoverySpec, data string) bool {
	if b == nil || b.Workspace == nil || workspacepolicy.Validate(b) != nil {
		return false
	}
	w := b.Workspace
	if b.Key != spec.Key || b.Path != filepath.Join(data, "benches", spec.Key) || w.CreationID != creationID || w.ExplicitQuotaBytes != spec.QuotaBytes || !reflect.DeepEqual(w.RecoveredFrom, &spec.Source) || len(w.Members) != len(spec.Repositories) {
		return false
	}
	for i, member := range w.Members {
		r := spec.Repositories[i]
		if member.Name != r.Name || member.Repository != r.Repository || member.Branch != r.Branch {
			return false
		}
	}
	return true
}

// StageRecovery forks a sealed source into a new identity and initialization
// owner. The shared staging sequence persists every source and provenance field
// before allocation/materialization. Retries consume that durable vector.
func (m Manager) StageRecovery(ctx context.Context, creationID string, spec RecoverySpec) (*model.Bench, error) {
	spec, err := recoveryRequest(creationID, spec)
	if err != nil {
		return nil, err
	}
	return m.stage(ctx, stagingRequest{
		key: spec.Key, creationID: creationID, explicitQuota: spec.QuotaBytes, recoveredFrom: &spec.Source,
		matches: func(b *model.Bench) bool { return matchesRecovery(b, creationID, spec, m.Data) },
		selectPlan: func(ctx context.Context, timeout time.Duration) (Plan, error) {
			return m.recoveryPlan(ctx, timeout, spec)
		},
	})
}

func (m Manager) recoveryPlan(ctx context.Context, timeout time.Duration, spec RecoverySpec) (Plan, error) {
	return m.recoveryPlanAuthorized(ctx, timeout, spec, false)
}

func (m Manager) recoveryPlanAuthorized(ctx context.Context, timeout time.Duration, spec RecoverySpec, transferred bool) (Plan, error) {
	var none Plan
	mirrors := repository.Mirrors{State: m.State, Data: m.Data, Timeout: timeout}
	configured := map[string]string{}
	for _, member := range spec.Repositories {
		if _, ok := configured[member.Repository]; ok {
			continue
		}
		path, err := mirrors.Prepare(ctx, member.Repository)
		if err != nil {
			return none, err
		}
		configured[member.Repository] = path
	}
	c := checkpointset.Coordinator{Mirrors: configured}
	source := spec.Source
	manifest, err := c.Recover(ctx, source.Repository, source.BenchID, source.Commit, source.ManifestSHA256)
	if err != nil {
		return none, err
	}
	if len(manifest.Members) != len(spec.Repositories) {
		return none, errors.New("recovery allowlist differs from the sealed member set")
	}
	for i, member := range manifest.Members {
		r := spec.Repositories[i]
		if member.Name != r.Name || member.Repository != r.Repository {
			return none, errors.New("recovery allowlist differs from sealed member identities")
		}
	}
	meta, _ := checkpointset.MetadataRef(source.BenchID)
	for _, target := range spec.Repositories {
		ref := "refs/heads/" + target.Branch
		for _, old := range manifest.Members {
			if !transferred && target.Repository == old.Repository && (ref == old.TaskRef || branchNamespaceConflict(ref, old.TaskRef)) {
				return none, errors.New("recovery requires distinct task refs; original writer ownership is not transferred")
			}
		}
		if target.Repository == source.Repository && (ref == meta || branchNamespaceConflict(ref, meta)) {
			return none, errors.New("recovery task ref conflicts with source checkpoint metadata")
		}
	}
	if transferred {
		for _, member := range manifest.Members {
			local, e := m.Git(ctx, configured[member.Repository], "rev-parse", "--verify", member.TaskRef)
			if e != nil || strings.TrimSpace(local) != member.Commit {
				return none, errors.New("transferred local task ref changed before destination admission")
			}
			remote, e := m.Git(ctx, configured[member.Repository], "ls-remote", "--refs", "origin", member.TaskRef)
			fields := strings.Fields(remote)
			if e != nil || len(fields) != 2 || fields[0] != member.Commit || fields[1] != member.TaskRef {
				return none, errors.New("transferred remote task ref changed before destination admission")
			}
		}
	}
	plan := Plan{Key: spec.Key, Root: filepath.Join(m.Data, "benches", spec.Key), CheckpointRepository: manifest.CheckpointRepository, QuotaBytes: spec.QuotaBytes}
	for i, member := range manifest.Members {
		mirror := configured[member.Repository]
		quota, err := setup.QuotaAtContext(ctx, mirror, member.Commit)
		if err != nil {
			return none, err
		}
		if quota != 0 {
			if plan.QuotaBytes != 0 && plan.QuotaBytes != quota {
				return none, errors.New("recovered repository quota declarations conflict with the total budget")
			}
			plan.QuotaBytes = quota
		}
		plan.Members = append(plan.Members, Member{Name: member.Name, Repository: member.Repository, Branch: spec.Repositories[i].Branch,
			// Base is derived provenance, never a recovery source selector.
			Base: strings.TrimPrefix(member.TaskRef, "refs/heads/"), Path: filepath.Join(plan.Root, member.Name), Mirror: mirror,
			Source: member.Commit, ObjectFormat: member.ObjectFormat, RepositoryQuotaBytes: quota})
	}
	return plan, nil
}
