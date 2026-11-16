//go:build linux

package payloadbootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperationjournal"
)

const InspectionVersion = "superbench.payload-bootstrap-inspection.v1"

// Inspection binds retained intent bytes to the original operation and input.
// Recorded nodes/allocation/phase describe the intent, not a current filesystem,
// kernel quota, stopped-controller or completion proof. This diagnostic grants
// no cleanup, quota release, retry or adoption authority.
type Inspection struct {
	Version, Key, ID, SpecSHA256, IntentSHA256, PolicyDigest, Phase string
	Operation                                                       quotaoperation.Identity
	OperationDenied                                                 bool
	RecordedAllocation                                              quotahelper.Assignment
	RecordedRoot, RecordedControls                                  payloadexec.Node
	CommandSHA256                                                   string
	CommandCompleted                                                bool
}

// Inspect reads an existing strict intent under the original operation lock,
// including after cancellation. Unknown identities and missing/partial/foreign
// intents refuse; nothing is initialized, reserved or changed to admitting.
// A future maintenance action must independently validate current storage and
// controller custody, then recheck this evidence under the same exclusion.
func (m *Manager) Inspect(ctx context.Context, s Spec) (Inspection, error) {
	if m == nil || m.store == nil || s.Validate() != nil {
		return Inspection{}, ErrBootstrap
	}
	var result Inspection
	err := m.store.Inspect(ctx, func(o quotaoperationjournal.Observation) error {
		if !o.Found || o.Identity != m.operation {
			return ErrBootstrap
		}
		i, err := m.read(s)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(i)
		if err != nil {
			return err
		}
		spec, err := s.Bytes()
		if err != nil {
			return err
		}
		intentSum, specSum := sha256.Sum256(raw), sha256.Sum256(spec)
		result = Inspection{Version: InspectionVersion, Key: s.Key, ID: s.ID,
			SpecSHA256: hex.EncodeToString(specSum[:]), IntentSHA256: hex.EncodeToString(intentSum[:]),
			PolicyDigest: i.PolicyDigest, Phase: i.Phase, Operation: o.Identity, OperationDenied: o.Denied,
			RecordedAllocation: i.Allocation, RecordedRoot: i.Root, RecordedControls: i.Controls,
			CommandSHA256: i.CommandSHA256, CommandCompleted: i.CommandCompleted}
		return m.check()
	})
	if err != nil {
		return Inspection{}, err
	}
	return result, nil
}
