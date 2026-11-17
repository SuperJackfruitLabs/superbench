//go:build linux

package payloadbootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storagecustody"
)

// Reclamation is what one reclaim step sees under Reclaim: the policies, the
// retained intent of the bench's completed bootstrap, Advance (completed ->
// reclaiming -> reclaimed only) and the policy recheck. It grants no Begin,
// Apply or adoption, and is void once Reclaim returns.
type Reclamation struct {
	Storage    hoststorage.Policy
	Payload    payloadexec.Policy
	Inspection func() (Inspection, error)
	Advance    func(phase string) error
	Check      func() error
}

// Reclaim runs action under this reclaim operation's reservation (Apply), which
// holds operations.lock against every bootstrap, recovery and retirement step.
// The daemon starts it through the fixed helper's sudo route; retries reserve
// the same identity again.
func (m *Manager) Reclaim(ctx context.Context, s Spec, action func(Reclamation) error) error {
	if m == nil || m.store == nil || s.Validate() != nil || action == nil {
		return ErrBootstrap
	}
	r := *m
	r.reclaim = true
	held := true
	live := func(f func() error) error {
		if !held {
			return ErrBootstrap
		}
		return f()
	}
	rec := Reclamation{Storage: r.storage, Payload: r.policy, Check: func() error { return live(r.check) }}
	rec.Inspection = func() (i Inspection, err error) {
		err = live(func() error { i, err = r.inspectionHeld(s, false); return err })
		return i, err
	}
	rec.Advance = func(phase string) error { return live(func() error { return r.advance(s, phase) }) }
	defer func() { held = false }()
	return m.store.Apply(ctx, func() error { return action(rec) })
}

// advance writes the intent's next reclaim phase. A transition file a crash
// left before its rename completes only if it holds exactly this transition.
func (m *Manager) advance(s Spec, phase string) error {
	i, err := m.read(s)
	if err != nil {
		return err
	}
	if !reclaimPhase(i.Phase, phase) {
		return ErrBootstrap
	}
	if i.Phase == phase {
		return nil
	}
	i.Phase = phase
	if err = m.write(i, false); !errors.Is(err, os.ErrExist) {
		return err
	}
	// The next read requires the renamed intent's single link and exact mode.
	want, err := json.Marshal(i)
	if err != nil {
		return err
	}
	path := m.intentPath(s)
	if got, err := storagecustody.ReadProtected(path+".transition", maxIntent); err != nil || !bytes.Equal(got, want) {
		return errors.Join(ErrBootstrap, err)
	}
	if err = os.Rename(path+".transition", path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	if err = d.Sync(); err != nil {
		return err
	}
	return m.check()
}
