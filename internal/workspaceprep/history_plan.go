package workspaceprep

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
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
)

const MaxHistoryPlan = 65536

var historyDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

// HistoryPlan approves one post-fence observation, not a recovered command
// outcome. Scope and private-row digests prevent approval reuse for a new owner.
// No lease capability, continuation, command arguments or environment is copied.
type HistoryPlan struct {
	Version, Root, Key, BenchID string
	BenchDigest, PriorDigest    string
	Execution                   execution.Execution
	ObservedAt                  time.Time
	File                        *reap.Entry
	Size                        int64
	Digest                      string
}

func (p HistoryPlan) hash() (string, error) {
	p.Digest = ""
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func privateHistoryDigest(b *model.Bench) (string, error) {
	raw, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (p HistoryPlan) validate() error {
	hash, err := p.hash()
	_, offset := p.ObservedAt.Zone()
	e := p.Execution
	if err != nil || p.Version != "superbench.workspace-history.v1" || p.Digest != hash || !historyDigest.MatchString(p.BenchDigest) || !historyDigest.MatchString(p.PriorDigest) || !history.ID.MatchString(p.BenchID) || !history.ID.MatchString(e.ID) || p.Key == "" || len(p.Key) > 80 || p.ObservedAt.IsZero() || offset != 0 || !filepath.IsAbs(p.Root) || filepath.Clean(p.Root) != p.Root || !utf8.ValidString(p.Root) || len(p.Root) > 4096 || e.Phase != "stopped" || e.Log != filepath.Join(p.Root, "run-"+e.ID+".log") || e.LogLimitBytes < 1024 || e.LogLimitBytes > 1<<30 || p.Size < 0 || uint64(p.Size) > e.LogLimitBytes {
		return errors.New("invalid workspace history approval")
	}
	if p.File == nil {
		if p.Size != 0 {
			return errors.New("absent capture has no file size")
		}
	} else if p.File.Path != "run-"+e.ID+".log" || !historyDigest.MatchString(p.File.SHA256) {
		return errors.New("invalid observed workspace log")
	}
	return nil
}

func (p HistoryPlan) Bytes() ([]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	if len(raw)+1 > MaxHistoryPlan {
		return nil, errors.New("workspace history approval exceeds limit")
	}
	return append(raw, '\n'), nil
}

// ParseHistoryPlan accepts the canonical generated plan, with optional JSON
// whitespace. Re-encoding rejects duplicate/unknown fields, null scalar values,
// repaired Unicode, alternate numbers and ambiguous representations.
func ParseHistoryPlan(raw []byte) (HistoryPlan, error) {
	var p HistoryPlan
	if len(raw) > MaxHistoryPlan || !utf8.Valid(raw) {
		return p, errors.New("invalid workspace history plan size or encoding")
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
	if err != nil || !bytes.Equal(compact.Bytes(), canonical) {
		return p, errors.New("workspace history plan must retain its canonical fields")
	}
	return p, p.validate()
}

func (p HistoryPlan) observation() history.InterruptedObservation {
	state := "absent"
	if p.File != nil {
		state = "present"
	}
	return history.InterruptedObservation{ApprovalDigest: p.Digest, PriorDigest: p.PriorDigest, LogState: state, ObservedAt: p.ObservedAt}
}
