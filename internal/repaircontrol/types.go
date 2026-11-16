// Package repaircontrol records the independent effect admission for an
// explicitly approved local repair. It grants no workspace or repair authority.
package repaircontrol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

// MaxRecords bounds unresolved admissions, not permanent terminal history.
const MaxRecords = 10000
const MaxRecord = 65536

var ErrAuthority = errors.New("local repair admission authority changed")
var ErrCancelled = errors.New("local repair cancellation requested")
var ErrRunning = errors.New("local repair controller requires reconciliation")
var id32 = regexp.MustCompile(`^[a-f0-9]{32}$`)
var digest64 = regexp.MustCompile(`^[a-f0-9]{64}$`)
var requestID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)

// Admission is immutable and capability-free. The core claim must pin this
// entire record, including the independent journal's incarnation, before any
// effect is admitted. Its random row ID is never a caller-selected repair ID.
type Admission struct {
	Instance, ID, BenchID, RepairID, Approval, OwnerDigest string
	Intent                                                 operationcontrol.Intent
	Control                                                operationcontrol.Control
}

func (a Admission) Valid() bool {
	return id32.MatchString(a.Instance) && id32.MatchString(a.ID) && id32.MatchString(a.BenchID) && requestID.MatchString(a.RepairID) && digest64.MatchString(a.Approval) && digest64.MatchString(a.OwnerDigest) && a.Intent.Valid() && a.Intent.Operation == "initialization-repair" && a.Control.Managed()
}

type Record struct {
	Admission         Admission
	Phase, Controller string
}

func (r Record) valid() bool {
	if !r.Admission.Valid() {
		return false
	}
	switch r.Phase {
	case "prepared":
		return r.Controller == ""
	case "running", "completed":
		return id32.MatchString(r.Controller)
	case "cancel_requested", "cancelled":
		return r.Controller == "" || id32.MatchString(r.Controller)
	}
	return false
}

func canonical(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxRecord {
		return nil, ErrAuthority
	}
	return raw, nil
}

func Digest(a Admission) string {
	raw, _ := canonical(a)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
