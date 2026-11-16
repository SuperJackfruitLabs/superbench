//go:build linux

package payloadbootstrap

import (
	"context"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
)

func (m *Manager) validAllocation(s Spec, a quotahelper.Assignment) bool {
	bytes := s.QuotaBytes
	if bytes == 0 {
		bytes = m.storage.BenchBytes
	}
	return a.Path == m.root(s) && a.ProjectID >= 10000 && a.Bytes == bytes && a.Inodes == m.storage.BenchInodes
}

// Bootstrap returns quota metadata from the retained fresh allocation, checked
// against the actual read-only ledger and enforcing XFS quotas after transfer.
// It never calls ordinary Apply or adopts a completed/interrupted allocation.
func (m *Manager) Bootstrap(ctx context.Context, s Spec) (Result, error) {
	var zero Result
	if err := m.Begin(ctx, s); err != nil {
		return zero, err
	}
	if err := m.Materialize(ctx, s); err != nil {
		return zero, err
	}
	layout, err := m.Complete(ctx, s)
	if err != nil {
		return zero, err
	}
	var result Result
	err = m.store.Apply(ctx, func() error {
		i, err := m.read(s)
		if err != nil {
			return err
		}
		if i.Phase != "completed" || m.checkRoot(i, m.policy.PayloadUID) != nil {
			return ErrBootstrap
		}
		actual, err := m.storage.VerifyStorage(ctx, s.Key)
		if err != nil {
			return err
		}
		if actual.Assignment != i.Allocation {
			return ErrBootstrap
		}
		result = Result{Version: ResultVersion, Layout: layout, Allocation: i.Allocation}
		if err := result.Validate(m.policy, s); err != nil {
			return err
		}
		return m.check()
	})
	if err != nil {
		return zero, err
	}
	return result, nil
}
