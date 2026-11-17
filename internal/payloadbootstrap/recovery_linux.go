//go:build linux

package payloadbootstrap

import (
	"context"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
)

// Recovery is what one Fresh recovery step sees while Recover holds the original
// operation's lock and permanent denial: the installed storage policy (its owner
// is the daemon), the retained inspection, the stopped-service gate and the policy
// recheck. It grants no Begin, Apply, adoption or retry, and is void afterwards.
type Recovery struct {
	Storage     hoststorage.Policy
	Inspection  func() (Inspection, error)
	Gate, Check func() error
}

// Recover runs action as direct root inside MaintainDenied for this identity.
func (m *Manager) Recover(ctx context.Context, s Spec, action func(Recovery) error) error {
	if err := m.retirementReady(ctx, s); err != nil || action == nil {
		return ErrBootstrap
	}
	held := true
	live := func(f func() error) func() error {
		return func() error {
			if !held {
				return ErrBootstrap
			}
			return f()
		}
	}
	r := Recovery{Storage: m.storage, Gate: live(func() error { return m.maintenanceGate(ctx, m.storage) }), Check: live(m.check)}
	r.Inspection = func() (i Inspection, err error) {
		err = live(func() error { i, err = m.inspectionHeld(s, true); return err })()
		return i, err
	}
	defer func() { held = false }()
	return m.store.MaintainDenied(ctx, func() error { return action(r) })
}
