//go:build linux

package freshrecovery

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshretirement"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
)

const (
	LeafVersion   = "superbench.fresh-leaf-removal.v1"
	RepairVersion = "superbench.fresh-quota-repair.v1"
	ResultVersion = "superbench.fresh-recovery-result.v1"
	// MaxPlan bounds a printed or retained plan; MaxRequest bounds {Spec, Plan}.
	MaxPlan    = 64 << 10
	MaxRequest = payloadbootstrap.MaxSpec + MaxPlan
)

// LeafPlan is the reviewed removal of one empty Fresh leaf and, for allocating
// (b), the fixed directories present beneath it. Bootstrap binds the retained
// intent; Storage binds the selected allocation, its authority and the pinned
// benches parent; Leaf is the exact observed leaf. It is not a cleanup plan:
// a leaf with anything but fixed empty directories is refused.
type LeafPlan struct {
	Version   string
	Bootstrap payloadbootstrap.Inspection
	Storage   freshretirement.Approval
	Leaf      Leaf
}

// Limit is the exact quota limit command a repair runs: the original caps.
type Limit struct {
	ProjectID               uint32
	BlockHardKiB, InodeHard uint64
}

// RepairPlan is the reviewed setting of a reserved project's never-set limits.
type RepairPlan struct {
	Version        string
	Bootstrap      payloadbootstrap.Inspection
	Storage        freshretirement.Approval
	FilesystemUUID string
	Limit          Limit
}

// Result is the same for an effect and for its exact retry after a lost reply.
type Result struct {
	Version, Key, Step, ApprovalSHA256 string
}

func digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

// bound checks the provenance and storage binding every plan shares, as the
// absent-retirement plan does. It needs the installed storage policy.
func bound(s payloadbootstrap.Spec, p hoststorage.Policy, b payloadbootstrap.Inspection, a freshretirement.Approval) error {
	raw, err := s.Bytes()
	if err != nil || b.Version != payloadbootstrap.InspectionVersion || !b.OperationDenied || b.Key != s.Key || b.ID != s.ID || b.SpecSHA256 != digest(raw) || len(b.IntentSHA256) != 64 {
		return ErrRecovery
	}
	if a.Validate(p) != nil || a.Version != freshretirement.Version || a.Key != s.Key || a.Authority != b.Operation.Authority {
		return ErrRecovery
	}
	want := s.QuotaBytes
	if want == 0 {
		want = p.BenchBytes
	}
	if a.Assignment.Bytes != want {
		return ErrRecovery
	}
	// A reserved intent precedes allocation; never fill it from the ledger.
	if b.Phase == "reserved" {
		if b.RecordedAllocation != (hoststorage.Assignment{}) || b.RecordedRoot.Device != 0 || b.RecordedRoot.Inode != 0 {
			return ErrRecovery
		}
	} else if b.RecordedAllocation != a.Assignment {
		return ErrRecovery
	}
	return nil
}

func (l LeafPlan) validate(s payloadbootstrap.Spec, p hoststorage.Policy) error {
	if l.Version != LeafVersion || bound(s, p, l.Bootstrap, l.Storage) != nil {
		return ErrRecovery
	}
	o := owners{uint32(p.OwnerUID), uint32(p.OwnerGID)}
	recorded := [2]uint64{l.Bootstrap.RecordedRoot.Device, l.Bootstrap.RecordedRoot.Inode}
	return leafAllowed(l.Bootstrap.Phase, recorded, l.Storage.Assignment.ProjectID, o, l.Storage.Parent.Device, payloadbootstrap.FixedDirectories(s), l.Leaf)
}

func (r RepairPlan) validate(s payloadbootstrap.Spec, p hoststorage.Policy) error {
	a := r.Storage.Assignment
	if r.Version != RepairVersion || r.Bootstrap.Phase != "reserved" || bound(s, p, r.Bootstrap, r.Storage) != nil || r.FilesystemUUID == "" {
		return ErrRecovery
	}
	if r.Limit != (Limit{a.ProjectID, a.Bytes / 1024, a.Inodes}) {
		return ErrRecovery
	}
	return nil
}

// ParseLeaf and ParseRepair accept only the canonical bounded {Spec, Plan}
// before any lock; the effect validates the plan again under the lock.
func ParseLeaf(raw []byte) (payloadbootstrap.Spec, LeafPlan, error) {
	var request struct {
		Spec payloadbootstrap.Spec
		Plan LeafPlan
	}
	err := canonical(raw, &request)
	if err != nil || request.Spec.Validate() != nil || request.Plan.Version != LeafVersion || request.Plan.Bootstrap.Key != request.Spec.Key {
		return payloadbootstrap.Spec{}, LeafPlan{}, ErrRecovery
	}
	return request.Spec, request.Plan, nil
}

func ParseRepair(raw []byte) (payloadbootstrap.Spec, RepairPlan, error) {
	var request struct {
		Spec payloadbootstrap.Spec
		Plan RepairPlan
	}
	err := canonical(raw, &request)
	if err != nil || request.Spec.Validate() != nil || request.Plan.Version != RepairVersion || request.Plan.Bootstrap.Key != request.Spec.Key {
		return payloadbootstrap.Spec{}, RepairPlan{}, ErrRecovery
	}
	return request.Spec, request.Plan, nil
}

// ParseClean accepts only the canonical bounded {Spec, PlanSHA256}: the plan
// itself is read from LEDGER/payload-cleanups under the lock, by that digest.
func ParseClean(raw []byte) (payloadbootstrap.Spec, string, error) {
	var request struct {
		Spec       payloadbootstrap.Spec
		PlanSHA256 string
	}
	err := canonical(raw, &request)
	if err != nil || request.Spec.Validate() != nil || len(request.PlanSHA256) != 64 || !isHex(request.PlanSHA256) {
		return payloadbootstrap.Spec{}, "", ErrRecovery
	}
	return request.Spec, request.PlanSHA256, nil
}

// canonical decodes raw only if it is exactly the compact encoding of its value,
// so no unknown, reordered or duplicate field survives.
func canonical[T any](raw []byte, request *T) error {
	if len(raw) > MaxRequest || !utf8.Valid(raw) || json.Unmarshal(raw, request) != nil {
		return ErrRecovery
	}
	again, err := json.Marshal(request)
	var compact bytes.Buffer
	if err != nil || json.Compact(&compact, raw) != nil || !bytes.Equal(again, compact.Bytes()) {
		return ErrRecovery
	}
	return nil
}

// encode is the exact retained and printed form of a plan.
func encode(plan any) ([]byte, error) {
	raw, err := json.Marshal(plan)
	if err != nil || len(raw) > MaxPlan {
		return nil, ErrRecovery
	}
	return raw, nil
}
