// Package workspacerepair owns explicitly approved local initialization repair.
package workspacerepair

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"time"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceprep"
)

const Version = "superbench.initialization-repair.v1"
const MaxPlan = 16 << 20
const MaxResult = 65536

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var identity = regexp.MustCompile(`^[a-f0-9]{32}$`)
var requestID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)

type HistoryBinding struct {
	ID, Digest       string
	Retired, Unknown bool
	ExitCode         *int
	RecordedFile     *reap.Entry
	RecordedSize     int64
	Observed         bool
	ObservedFile     *reap.Entry
	ObservedSize     int64
}

// Plan contains decisions and evidence, never an owner capability or command.
type Plan struct {
	Version, Root, DataRoot, Key, BenchID, CreationID, Phase string
	PreviousID, BenchDigest                                  string
	ObservedAt                                               time.Time
	Execution                                                *execution.Execution
	Pending                                                  *model.WorkspaceCheckpoint
	CurrentVector                                            []model.WorkspaceCommit
	Members                                                  []workspaceprep.RepairMember
	History                                                  []HistoryBinding
	Digest                                                   string
}

func hash(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return hashBytes(raw), nil
}
func hashBytes(raw []byte) string    { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func (p Plan) hash() (string, error) { p.Digest = ""; return hash(p) }

func (p Plan) validate() error {
	digest, err := p.hash()
	_, offset := p.ObservedAt.Zone()
	if err != nil || p.Version != Version || digest != p.Digest || !digestPattern.MatchString(p.BenchDigest) || !identity.MatchString(p.BenchID) || !requestID.MatchString(p.Key) || !requestID.MatchString(p.CreationID) || p.PreviousID != "" && !requestID.MatchString(p.PreviousID) || p.Phase != "initializing" && p.Phase != "prepared" || p.ObservedAt.IsZero() || offset != 0 || len(p.Members) < 1 || len(p.Members) > 32 || len(p.History) > 10000 {
		return errors.New("invalid initialization repair plan")
	}
	for _, path := range []string{p.Root, p.DataRoot} {
		if !utf8.ValidString(path) || len(path) > 4096 || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
			return errors.New("invalid initialization repair storage root")
		}
	}
	if len(p.CurrentVector) != len(p.Members) {
		return errors.New("incomplete initialization repair vector")
	}
	for i, m := range p.Members {
		if p.CurrentVector[i].Name != m.Name || !model.ValidCommit(p.CurrentVector[i].Commit) {
			return errors.New("invalid initialization repair HEAD vector")
		}
		if m.Name == "" || i > 0 && p.Members[i-1].Name >= m.Name || !digestPattern.MatchString(m.Digest) || !digestPattern.MatchString(m.ReceiptDigest) || m.RunID != "" && !identity.MatchString(m.RunID) {
			return errors.New("invalid initialization preparation decision")
		}
		switch m.Action {
		case "preserve", "salvage", "prepare", "rerun-once":
		default:
			return errors.New("unknown initialization preparation decision")
		}
		if (m.Action == "prepare" || m.Action == "rerun-once") && m.RunID != "" {
			return errors.New("unexecuted preparation has no run proof")
		}
	}
	for i, h := range p.History {
		if !identity.MatchString(h.ID) || !digestPattern.MatchString(h.Digest) || i > 0 && p.History[i-1].ID >= h.ID || h.RecordedSize < 0 || h.ObservedSize < 0 || h.Retired == h.Observed || h.Unknown && h.ExitCode != nil {
			return errors.New("invalid initialization history binding")
		}
	}
	return nil
}

func (p Plan) Bytes() ([]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	if len(raw)+1 > MaxPlan {
		return nil, errors.New("initialization repair plan exceeds limit")
	}
	return append(raw, '\n'), nil
}

// ParsePlan permits formatting whitespace but retains canonical fields/order,
// rejecting duplicate/unknown keys, repaired Unicode and ambiguous numbers.
func ParsePlan(raw []byte) (Plan, error) {
	var p Plan
	if len(raw) > MaxPlan || !utf8.Valid(raw) {
		return p, errors.New("invalid initialization plan size or encoding")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return p, err
	}
	canonical, err := json.Marshal(p)
	if err != nil || !bytes.Equal(canonical, compact.Bytes()) {
		return p, errors.New("initialization plan must retain its canonical fields")
	}
	return p, p.validate()
}
