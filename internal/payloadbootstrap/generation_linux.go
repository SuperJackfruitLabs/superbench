//go:build linux

package payloadbootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshgeneration"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshretirement"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storagecustody"
)

// predecessor is the reclaimed or root-retired generation a later one follows
// (freshgeneration.Follows); its allocation is the key's tombstone.
func (m *Manager) predecessor(ctx context.Context, s Spec) (hoststorage.Assignment, error) {
	prior, plan, err := ReadPrior(m.storage.Ledger, s.Key, freshgeneration.Before(s.Generation))
	if err == nil {
		err = freshgeneration.Follows(prior, s.Key, s.Previous, s.Generation, m.operation.Authority, m.root(s))
	}
	if err == nil && plan != nil {
		err = freshretirement.Retired(ctx, m.storage, plan.Storage)
	}
	if err != nil {
		return hoststorage.Assignment{}, errors.Join(ErrBootstrap, err)
	}
	return hoststorage.Assignment(prior.Allocation()), nil
}

// ReadPrior reads key's generation's intent and, unless reclaimed, root's retirement.
func ReadPrior(ledger, key string, generation uint32) (freshgeneration.Prior, *RetirementPlan, error) {
	var i intent
	raw, err := storagecustody.ReadProtected(filepath.Join(ledger, "payload-bootstrap", freshgeneration.Stem(key, generation)+".json"), maxIntent)
	err = errors.Join(err, json.Unmarshal(raw, &i))
	canonical, _ := json.Marshal(i)
	spec, specErr := i.Spec.Bytes()
	if err != nil || specErr != nil || !bytes.Equal(raw, canonical) || i.Spec.Key != key || i.Spec.Generation != generation {
		return freshgeneration.Prior{}, nil, errors.Join(ErrBootstrap, err)
	}
	prior := freshgeneration.Prior{Key: i.Spec.Key, ID: i.Spec.ID, Generation: i.Spec.Generation, Phase: i.Phase, Authority: i.Operation.Authority, Path: i.Allocation.Path, ProjectID: i.Allocation.ProjectID,
		Bytes: i.Allocation.Bytes, Inodes: i.Allocation.Inodes, Operation: fmt.Sprint(i.Operation), PolicyDigest: i.PolicyDigest, IntentSHA256: digestBytes(raw), SpecSHA256: digestBytes(spec)}
	if i.Phase == "reclaimed" {
		return prior, nil, nil
	}
	var plan RetirementPlan
	if raw, err = storagecustody.ReadProtected(freshgeneration.RetirementPath(ledger, key, generation), MaxRetirement); err == nil {
		err = json.Unmarshal(raw, &plan)
	}
	canonical, _ = json.Marshal(plan)
	b, a := plan.Bootstrap, plan.Storage
	if err != nil || !bytes.Equal(raw, canonical) || plan.Version != RetirementVersion || b.Version != InspectionVersion || a.Version != freshretirement.Version {
		return freshgeneration.Prior{}, nil, errors.Join(ErrBootstrap, err)
	}
	prior.Retirement = &freshgeneration.Retirement{Key: b.Key, ID: b.ID, Phase: b.Phase, IntentSHA256: b.IntentSHA256, SpecSHA256: b.SpecSHA256, Operation: fmt.Sprint(b.Operation), PolicyDigest: b.PolicyDigest,
		OperationDenied: b.OperationDenied, Recorded: freshgeneration.Allocation(b.RecordedAllocation), ApprovalKey: a.Key, Authority: a.Authority, Approved: freshgeneration.Allocation(a.Assignment)}
	return prior, &plan, nil
}
