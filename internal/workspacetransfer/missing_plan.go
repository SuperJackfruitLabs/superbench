package workspacetransfer

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotapolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/worktreetransfer"
)

const MissingVersion = "superbench.workspace-missing-transfer.v1"

// MissingMemberPlan explicitly distinguishes a retained checkout from an absent
// path. Absence is a storage-bound observation, never an empty checkout snapshot.
type MissingMemberPlan struct {
	Name, IntegrationBase string
	Present               *worktreetransfer.Plan        `json:",omitempty"`
	Missing               *worktreetransfer.MissingPlan `json:",omitempty"`
}

// MissingPlan approves recovery of the complete old seal, not recovery of lost
// uncommitted files or successful completion of the retired owner's commands.
type MissingPlan struct {
	Version, Root, DataRoot                                  string
	ID, SourceKey, SourceID, TargetKey, TargetID, CreationID string
	SourceRowDigest, CheckpointRepository                    string
	ObservedAt                                               time.Time
	Checkpoint                                               *model.WorkspaceCheckpoint
	Repositories                                             []workspace.Repository
	QuotaBytes                                               uint64
	Storage                                                  hoststorage.StorageIdentity
	Recovery                                                 model.WorkspaceTransferRecovery
	History                                                  []TransferHistory
	Members                                                  []MissingMemberPlan
	Digest                                                   string
}

func (p MissingPlan) digest() (string, error) { p.Digest = ""; return hash(p) }
func (p MissingPlan) validate() error {
	d, err := p.digest()
	_, offset := p.ObservedAt.Zone()
	if err != nil || p.Version != MissingVersion || d != p.Digest || !digestPattern.MatchString(p.SourceRowDigest) || !keyPattern.MatchString(p.ID) || !keyPattern.MatchString(p.SourceKey) || !keyPattern.MatchString(p.TargetKey) || !keyPattern.MatchString(p.CreationID) || p.SourceKey == p.TargetKey || !identity.MatchString(p.SourceID) || !identity.MatchString(p.TargetID) || p.SourceID == p.TargetID || !quotapolicy.ValidBytes(p.QuotaBytes) || p.ObservedAt.IsZero() || offset != 0 || p.Checkpoint == nil || p.Checkpoint.Seal == "" || len(p.Members) < 1 || len(p.Members) > 32 || len(p.Repositories) != len(p.Members) || len(p.Checkpoint.Members) != len(p.Members) {
		return errors.New("invalid missing-checkout transfer approval")
	}
	if _, err = p.Storage.Bytes(); err != nil {
		return err
	}
	if p.Storage.Root != p.DataRoot || p.Storage.BenchKey != p.SourceKey || p.Recovery.Mode != "missing" || p.Recovery.SourceRowDigest != p.SourceRowDigest || p.Recovery.RetiredAt != p.ObservedAt.Format(time.RFC3339Nano) || len(p.History) != len(p.Recovery.Runs) {
		return errors.New("missing-checkout storage or retirement approval differs")
	}
	for i, h := range p.History {
		if !identity.MatchString(h.ID) || !digestPattern.MatchString(h.Digest) || i > 0 && p.History[i-1].ID >= h.ID || h.Limit == 0 || h.Limit > 1<<30 || h.Size < 0 || uint64(h.Size) > h.Limit || p.Recovery.Runs[i].ID != h.ID || p.Recovery.Runs[i].Digest != h.Digest || h.File == nil && h.Size != 0 || h.File != nil && h.File.Path != "run-"+h.ID+".log" {
			return errors.New("missing-checkout retained history differs")
		}
	}
	missing := false
	for i, member := range p.Members {
		if member.Name == "" || !model.ValidBranchRef("refs/heads/"+member.IntegrationBase) || i > 0 && p.Members[i-1].Name >= member.Name || p.Repositories[i].Name != member.Name || p.Repositories[i].Base != "" || p.Checkpoint.Members[i].Name != member.Name || (member.Present == nil) == (member.Missing == nil) {
			return errors.New("missing-checkout approval has an incomplete member vector")
		}
		path := filepath.Join(p.DataRoot, "benches", p.SourceKey, member.Name)
		ref, commit := "refs/heads/"+p.Repositories[i].Branch, p.Checkpoint.Members[i].Commit
		if member.Present != nil && (member.Present.Checkout != path || member.Present.TaskRef != ref || member.Present.Commit != commit) {
			return errors.New("present member differs from its registered source")
		}
		if member.Missing != nil {
			missing = true
			spec := member.Missing.Spec
			storage, _ := p.Storage.Bytes()
			if spec.ID != p.ID || spec.Checkout != path || spec.TaskRef != ref || spec.Commit != commit || !bytes.Equal(spec.Storage, storage) {
				return errors.New("missing member differs from its registered source")
			}
		}
	}
	if !missing {
		return errors.New("missing-checkout approval requires an observed absent member")
	}
	return nil
}
func (p MissingPlan) Bytes() ([]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(p)
	if len(raw)+1 > MaxPlan {
		return nil, errors.New("missing-checkout transfer approval exceeds its bound")
	}
	return append(raw, '\n'), err
}
func ParseMissingPlan(raw []byte) (MissingPlan, error) {
	var p MissingPlan
	if len(raw) > MaxPlan || !utf8.Valid(raw) {
		return p, errors.New("invalid missing-checkout transfer approval size or encoding")
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
		return p, errors.New("missing-checkout approval must retain canonical fields and order")
	}
	return p, p.validate()
}
