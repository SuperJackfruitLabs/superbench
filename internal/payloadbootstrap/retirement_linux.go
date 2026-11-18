//go:build linux

package payloadbootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshretirement"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
)

const RetirementVersion = "superbench.bootstrap-retirement.v1"

func digestBytes(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

// RetirementPlan binds historical provenance separately from current storage.
// It is not permission to re-run bootstrap or proof that materialization finished.
type RetirementPlan struct {
	Version   string
	Bootstrap Inspection
	Storage   freshretirement.Approval
}

func (p RetirementPlan) validate(s Spec, m *Manager) error {
	b, a := p.Bootstrap, p.Storage
	if m == nil || a.Validate(m.storage) != nil {
		return ErrBootstrap
	}
	if m == nil || s.Validate() != nil || p.Version != RetirementVersion || b.Version != InspectionVersion || !b.OperationDenied || b.Operation != m.operation || b.PolicyDigest != m.digest || b.Key != s.Key || b.ID != s.ID || !sha256Pattern.MatchString(b.IntentSHA256) || a.Key != s.Key || a.Authority != b.Operation.Authority || a.Version != freshretirement.Version {
		return ErrBootstrap
	}
	wantBytes := s.QuotaBytes
	if wantBytes == 0 {
		wantBytes = m.storage.BenchBytes
	}
	raw, err := s.Bytes()
	if err != nil || b.SpecSHA256 != digestBytes(raw) || a.Assignment.Bytes != wantBytes {
		return ErrBootstrap
	}
	// A reserved intent precedes allocation. Never fill its historical zero
	// assignment from the ledger or pretend this proves initialization completed.
	if b.Phase == "reserved" {
		if b.RecordedAllocation.ProjectID != 0 || b.RecordedAllocation.Path != "" || b.RecordedAllocation.Bytes != 0 || b.RecordedAllocation.Inodes != 0 {
			return ErrBootstrap
		}
	} else if b.RecordedAllocation.ProjectID != a.Assignment.ProjectID || b.RecordedAllocation.Path != a.Assignment.Path || b.RecordedAllocation.Bytes != a.Assignment.Bytes || b.RecordedAllocation.Inodes != a.Assignment.Inodes {
		return ErrBootstrap
	}
	return nil
}

func (m *Manager) retirementReady(ctx context.Context, s Spec) error {
	if m == nil || m.store == nil || s.Validate() != nil || os.Getuid() != 0 || os.Geteuid() != 0 || os.Getenv("SUDO_UID") != "" {
		return ErrBootstrap
	}
	return ctx.Err()
}

// PlanRetirement observes an absent reservation only while its original operation
// is denied and the actual installed controllers are stopped. It writes nothing.
func (m *Manager) PlanRetirement(ctx context.Context, s Spec) (RetirementPlan, error) {
	if err := m.retirementReady(ctx, s); err != nil {
		return RetirementPlan{}, err
	}
	var result RetirementPlan
	err := m.store.MaintainDenied(ctx, func() error {
		if err := m.maintenanceGate(ctx, m.storage); err != nil {
			return err
		}
		i, err := m.inspectionHeld(s, true)
		if err != nil {
			return err
		}
		a, err := freshretirement.ObserveAbsent(ctx, m.storage, s.Key)
		if err != nil {
			return err
		}
		result = RetirementPlan{RetirementVersion, i, a}
		if err = result.validate(s, m); err != nil {
			return err
		}
		if err = m.maintenanceGate(ctx, m.storage); err != nil {
			return err
		}
		return m.check()
	})
	if err != nil {
		return RetirementPlan{}, err
	}
	return result, nil
}

// RetireAbsent persists the exact reviewed plan before retirement. The original
// operation lock spans provenance, real controller checks, durable approval and
// the effect. Exact retries retain the same approval and permanent denial.
func (m *Manager) RetireAbsent(ctx context.Context, s Spec, plan RetirementPlan) (hoststorage.Assignment, error) {
	var result hoststorage.Assignment
	if err := m.retirementReady(ctx, s); err != nil {
		return result, err
	}
	if err := plan.validate(s, m); err != nil {
		return result, err
	}
	approvalRetained := false
	err := m.store.MaintainDenied(ctx, func() error {
		if err := m.maintenanceGate(ctx, m.storage); err != nil {
			return err
		}
		i, err := m.inspectionHeld(s, true)
		if err != nil {
			return err
		}
		if i != plan.Bootstrap {
			return ErrBootstrap
		}
		if err = m.retainRetirement(s, plan); err != nil {
			return err
		}
		approvalRetained = true
		if err = m.maintenanceGate(ctx, m.storage); err != nil {
			return err
		}
		// A refusal can follow a committed tombstone. Keep this exact durable
		// plan for retry; an error must never be interpreted as no effects.
		result, err = freshretirement.RetireAbsent(ctx, m.storage, plan.Storage)
		if err != nil {
			return err
		}
		if err = m.maintenanceGate(ctx, m.storage); err != nil {
			return err
		}
		after, err := m.inspectionHeld(s, true)
		if err != nil {
			return err
		}
		if after != i {
			return ErrBootstrap
		}
		return m.check()
	})
	if err != nil {
		if approvalRetained {
			err = fmt.Errorf("effects may have occurred; retry the same plan: %w", err)
		}
		return hoststorage.Assignment{}, err
	}
	return result, nil
}
