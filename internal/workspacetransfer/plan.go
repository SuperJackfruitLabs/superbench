// Package workspacetransfer owns reviewed original task-ref handoffs.
package workspacetransfer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"time"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotapolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reftransfer"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/worktreetransfer"
)

const Version = "superbench.workspace-transfer.v1"
const MaxPlan = reftransfer.MaxPlan
const MaxResult = 65536

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)
var identity = regexp.MustCompile(`^[a-f0-9]{32}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type MemberPlan struct {
	Name            string
	IntegrationBase string
	Worktree        worktreetransfer.Plan
}

// Plan binds an approval to one source and one fresh destination. It contains no
// capability, raw command, environment, or authority to discard source files.
type Plan struct {
	Version, Root, DataRoot                                  string
	ID, SourceKey, SourceID, TargetKey, TargetID, CreationID string
	SourceRowDigest, CheckpointRepository                    string
	ObservedAt                                               time.Time
	Checkpoint                                               *model.WorkspaceCheckpoint
	Repositories                                             []workspace.Repository
	QuotaBytes                                               uint64
	Members                                                  []MemberPlan
	Digest                                                   string
}

func hash(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func (p Plan) digest() (string, error) { p.Digest = ""; return hash(p) }
func (p Plan) validate() error {
	d, err := p.digest()
	_, offset := p.ObservedAt.Zone()
	if err != nil || p.Version != Version || d != p.Digest || !digestPattern.MatchString(p.SourceRowDigest) || !keyPattern.MatchString(p.ID) || !keyPattern.MatchString(p.SourceKey) || !keyPattern.MatchString(p.TargetKey) || !keyPattern.MatchString(p.CreationID) || p.SourceKey == p.TargetKey || !identity.MatchString(p.SourceID) || !identity.MatchString(p.TargetID) || p.SourceID == p.TargetID || !quotapolicy.ValidBytes(p.QuotaBytes) || p.ObservedAt.IsZero() || offset != 0 || p.Checkpoint == nil || p.Checkpoint.Seal == "" || len(p.Members) < 1 || len(p.Members) > 32 || len(p.Repositories) != len(p.Members) || len(p.Checkpoint.Members) != len(p.Members) {
		return errors.New("invalid workspace transfer approval")
	}
	for i, member := range p.Members {
		if member.Name == "" || !model.ValidBranchRef("refs/heads/"+member.IntegrationBase) || i > 0 && p.Members[i-1].Name >= member.Name || p.Repositories[i].Name != member.Name || p.Repositories[i].Base != "" || p.Checkpoint.Members[i].Name != member.Name || member.Worktree.Commit != p.Checkpoint.Members[i].Commit || member.Worktree.TaskRef != "refs/heads/"+p.Repositories[i].Branch {
			return errors.New("workspace transfer approval has an incomplete original-ref vector")
		}
	}
	return nil
}
func (p Plan) Bytes() ([]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(p)
	if len(raw)+1 > reftransfer.MaxPlan {
		return nil, errors.New("workspace transfer approval exceeds its bound")
	}
	return append(raw, '\n'), err
}
func ParsePlan(raw []byte) (Plan, error) {
	var p Plan
	if len(raw) > reftransfer.MaxPlan || !utf8.Valid(raw) {
		return p, errors.New("invalid workspace transfer approval size or encoding")
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
		return p, errors.New("workspace transfer approval must retain canonical fields and order")
	}
	return p, p.validate()
}
