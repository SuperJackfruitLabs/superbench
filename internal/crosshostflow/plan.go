// Package crosshostflow owns reviewed local administration of original refs
// across machines. Its artifacts are private evidence, never remote fencing.
package crosshostflow

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"time"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/crosshost"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/handoffjournal"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/worktreetransfer"
)

const Version = "superbench.cross-host-export-approval.v1"

var key = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)
var hex32 = regexp.MustCompile(`^[a-f0-9]{32}$`)
var hex64 = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Member struct {
	Name, Repository, Branch, IntegrationBase string
	Worktree                                  worktreetransfer.Plan
}
type Plan struct {
	Version, Root, DataRoot, ID, SourceKey, SourceID, SourceHost, SourceState, SourceRowDigest string
	Destination                                                                                handoffjournal.Offer
	ObservedAt                                                                                 time.Time
	CheckpointRepository                                                                       string
	Checkpoint                                                                                 *model.WorkspaceCheckpoint
	Members                                                                                    []Member
	Digest                                                                                     string
}

func hash(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (p Plan) valid() error {
	if _, err := OfferBytes(p.Destination); err != nil {
		return err
	}
	unsigned := p
	unsigned.Digest = ""
	_, zone := p.ObservedAt.Zone()
	if p.Version != Version || p.Digest != hash(unsigned) || !key.MatchString(p.ID) || !key.MatchString(p.SourceKey) || !hex32.MatchString(p.SourceID) || !hex32.MatchString(p.SourceState) || !hex64.MatchString(p.SourceHost) || !hex64.MatchString(p.SourceRowDigest) || p.ObservedAt.IsZero() || zone != 0 || len(p.Members) < 1 || len(p.Members) > 32 || p.Checkpoint == nil || len(p.Checkpoint.Members) != len(p.Members) {
		return errors.New("invalid cross-host export approval")
	}
	_, err := p.artifactUnchecked()
	return err
}
func (p Plan) artifactUnchecked() (crosshost.Artifact, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return crosshost.Artifact{}, err
	}
	sum := sha256.Sum256(raw)
	a := crosshost.Artifact{Version: crosshost.Version, Source: crosshost.Source{HostID: p.SourceHost, StateID: p.SourceState, WorkspaceID: p.SourceID, Key: p.SourceKey, TransferID: p.ID, ApprovalDigest: hex.EncodeToString(sum[:])}, Destination: p.Destination.Destination, CheckpointRepository: p.CheckpointRepository, Seal: p.Checkpoint.Seal, ManifestDigest: p.Checkpoint.Digest}
	for n, m := range p.Members {
		cp := p.Checkpoint.Members[n]
		if m.Name != cp.Name || m.Worktree.Commit != cp.Commit || m.Worktree.TaskRef != "refs/heads/"+m.Branch {
			return crosshost.Artifact{}, errors.New("export approval has an incomplete original-ref vector")
		}
		a.Members = append(a.Members, crosshost.Member{Name: m.Name, Repository: m.Repository, Branch: m.Branch, IntegrationBase: m.IntegrationBase, Commit: cp.Commit})
	}
	sealed, err := crosshost.Seal(a)
	if err != nil {
		return crosshost.Artifact{}, err
	}
	return crosshost.Parse(sealed)
}
func (p Plan) Artifact() (crosshost.Artifact, error) {
	if err := p.valid(); err != nil {
		return crosshost.Artifact{}, err
	}
	return p.artifactUnchecked()
}
func (p Plan) Bytes() ([]byte, error) {
	if err := p.valid(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(p)
	if len(raw) > handoffjournal.MaxApproval {
		return nil, errors.New("export approval exceeds bound")
	}
	return raw, err
}
func ParsePlan(raw []byte) (Plan, error) {
	var p Plan
	if len(raw) > handoffjournal.MaxApproval || !utf8.Valid(raw) {
		return p, errors.New("invalid export approval size or encoding")
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, err
	}
	expected, err := p.Bytes()
	if err != nil || !bytes.Equal(bytes.TrimSpace(raw), expected) {
		return Plan{}, errors.New("export approval must retain exact canonical fields")
	}
	return p, nil
}
