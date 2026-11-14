// Package maintenance runs narrowly scoped, explicitly approved retention rules.
package maintenance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/retention"
	"time"
)

const Scope = "registered-released-sealed-run-output"

type Store interface {
	PlanCollection(time.Duration, time.Duration, int) (*retention.Plan, error)
	Collect(retention.Plan) (retention.Collection, error)
	Collections() ([]retention.Collection, error)
}
type Policy struct {
	Version, Root, Scope string
	LogAge, MetadataAge  Duration
	Limit                int
	MaxBytes             uint64
}
type Proposal struct {
	Policy  Policy
	Initial retention.Plan
	Digest  string
}
type Config struct {
	Proposal  Proposal
	Enabled   bool
	EnabledAt time.Time
}
type Pending struct {
	PolicyDigest string
	Plan         retention.Plan
}
type Report struct {
	PolicyDigest, Status, Error string
	At                          time.Time
	Collection                  *retention.Collection
	Deferred                    []string
}
type Status struct {
	Config  *Config
	Pending *Pending
	Last    *Report
}
type Manager struct {
	Root  string
	Store Store
}

func (p Policy) Validate(root string) error {
	if p.Version != "superbench.retention-policy.v1" || p.Root != root || p.Scope != Scope || p.LogAge.Value() < time.Hour || p.MetadataAge.Value() < 24*time.Hour || p.Limit < 1 || p.Limit > retention.MaxItems || p.MaxBytes < 1024 || p.MaxBytes > 1<<40 {
		return errors.New("invalid automatic retention policy")
	}
	return nil
}
func (p Proposal) Hash() (string, error) {
	p.Digest = ""
	raw, e := json.Marshal(p)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func (p Proposal) Validate(root string) error {
	if e := p.Policy.Validate(root); e != nil {
		return e
	}
	if e := p.Initial.Validate(root); e != nil {
		return e
	}
	hash, e := p.Hash()
	if e != nil {
		return e
	}
	if hash != p.Digest {
		return errors.New("policy approval digest mismatch")
	}
	return validateBatch(p.Initial, p.Policy)
}
func bound(plan retention.Plan, p Policy) (retention.Plan, []string, error) {
	var size uint64
	logs := make([]retention.Item, 0, len(plan.Logs))
	var deferred []string
	for _, item := range plan.Logs {
		if item.Record.Size < 0 {
			return plan, nil, errors.New("invalid retained log size")
		}
		bytes := uint64(item.Record.Size)
		if bytes > p.MaxBytes-size {
			deferred = append(deferred, item.Record.ID)
			continue
		}
		size += bytes
		logs = append(logs, item)
	}
	plan.Logs = logs
	var e error
	plan.Digest, e = plan.Hash()
	return plan, deferred, e
}
func selection(plan retention.Plan) (string, error) {
	plan.CreatedAt = time.Time{}
	plan.LogCutoff = time.Time{}
	plan.MetadataCutoff = time.Time{}
	plan.Digest = ""
	return plan.Hash()
}
func (m Manager) Preview(p Policy) (*Proposal, error) {
	if e := p.Validate(m.Root); e != nil {
		return nil, e
	}
	plan, e := m.Store.PlanCollection(p.LogAge.Value(), p.MetadataAge.Value(), p.Limit)
	if e != nil {
		return nil, e
	}
	bounded, _, e := bound(*plan, p)
	if e != nil {
		return nil, e
	}
	proposal := &Proposal{Policy: p, Initial: bounded}
	proposal.Digest, e = proposal.Hash()
	return proposal, e
}
func (m Manager) Enable(p Proposal, ack bool) (Status, error) {
	if !ack {
		return Status{}, errors.New("automatic deletion requires explicit policy approval and enable acknowledgment")
	}
	if e := p.Validate(m.Root); e != nil {
		return Status{}, e
	}
	unlock, e := m.gate(false)
	if e != nil {
		return Status{}, e
	}
	defer unlock()
	var pending Pending
	if e = m.read("retention-auto-pending.json", &pending); e == nil {
		if pending.PolicyDigest != p.Digest {
			return Status{}, errors.New("another policy has a pending collection; reconcile it first")
		}
		if e = validateBatch(pending.Plan, p.Policy); e != nil {
			return Status{}, e
		}
		// Prepared records disappear from fresh candidate discovery. Reenabling
		// the exact approved policy resumes only its retained pending batch.
	} else if !missing(e) {
		return Status{}, e
	} else if e = m.checkInitial(p); e != nil {
		return Status{}, e
	}
	c := Config{Proposal: p, Enabled: true, EnabledAt: time.Now().UTC()}
	if e = m.write("retention-policy.json", c); e != nil {
		return Status{}, e
	}
	return m.Status()
}
func (m Manager) checkInitial(p Proposal) error {
	// The concrete first batch must still match. Future batches are authorized
	// only by the exact fixed scope/ages/bounds displayed in this proposal.
	fresh, e := m.Preview(p.Policy)
	if e != nil {
		return e
	}
	a, e := selection(p.Initial)
	if e != nil {
		return e
	}
	b, e := selection(fresh.Initial)
	if e != nil {
		return e
	}
	if a != b {
		return errors.New("retention candidates changed; review a fresh policy preview")
	}
	return nil
}
func (m Manager) Disable() (Status, error) {
	unlock, e := m.gate(false)
	if e != nil {
		return Status{}, e
	}
	defer unlock()
	var c Config
	if e = m.read("retention-policy.json", &c); missing(e) {
		return m.Status()
	} else if e != nil {
		return Status{}, e
	}
	if e = c.Proposal.Validate(m.Root); e != nil {
		return Status{}, e
	}
	c.Enabled = false
	if e = m.write("retention-policy.json", c); e != nil {
		return Status{}, e
	}
	return m.Status()
}

type Duration time.Duration

func (d Duration) Value() time.Duration         { return time.Duration(d) }
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.Value().String()) }
func (d *Duration) UnmarshalJSON(raw []byte) error {
	var text string
	if e := json.Unmarshal(raw, &text); e != nil {
		return e
	}
	value, e := time.ParseDuration(text)
	if e != nil {
		return e
	}
	*d = Duration(value)
	return nil
}
func validateBatch(plan retention.Plan, p Policy) error {
	if e := plan.Validate(p.Root); e != nil {
		return e
	}
	if !plan.LogCutoff.Equal(plan.CreatedAt.Add(-p.LogAge.Value())) || !plan.MetadataCutoff.Equal(plan.CreatedAt.Add(-p.MetadataAge.Value())) || len(plan.Logs)+len(plan.Records)+len(plan.Collections) > p.Limit {
		return errors.New("automatic batch differs from approved age/count rules")
	}
	bounded, _, e := bound(plan, p)
	if e != nil {
		return e
	}
	if bounded.Digest != plan.Digest {
		return errors.New("automatic batch exceeds approved byte bound")
	}
	for _, item := range plan.Collections {
		finished, e := time.Parse(time.RFC3339Nano, item.Finished)
		if e != nil {
			return e
		}
		if finished.After(plan.MetadataCutoff) {
			return errors.New("collection summary is newer than approved metadata age")
		}
	}
	return nil
}
