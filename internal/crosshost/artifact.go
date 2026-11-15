// Package crosshost describes private, reviewed original-ref handoff data.
// Parsing this data never proves source quiescence or grants writer authority.
package crosshost

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

const Version = "superbench.cross-host-handoff.v1"
const MaxArtifact = 256 << 10

type Destination struct {
	HostID, StateID, WorkspaceID, Key, CreationID string
}
type Source struct {
	HostID, StateID, WorkspaceID, Key, TransferID, ApprovalDigest string
}
type Member struct {
	Name, Repository, Branch, IntegrationBase, Commit string
}
type Artifact struct {
	Version                                    string
	Source                                     Source
	Destination                                Destination
	CheckpointRepository, Seal, ManifestDigest string
	Members                                    []Member
	Digest                                     string
}

var key = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)
var name = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func hexadecimal(s string, width int) bool {
	if len(s) != width || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func digest(a Artifact) (string, error) {
	a.Digest = ""
	raw, err := json.Marshal(a)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func destination(d Destination) bool {
	return hexadecimal(d.HostID, 64) && hexadecimal(d.StateID, 32) && hexadecimal(d.WorkspaceID, 32) && key.MatchString(d.Key) && key.MatchString(d.CreationID)
}

func (a Artifact) validate() error {
	invalid := errors.New("invalid cross-host handoff artifact")
	s, d := a.Source, a.Destination
	if a.Version != Version || !destination(d) || !hexadecimal(s.HostID, 64) || !hexadecimal(s.StateID, 32) || !hexadecimal(s.WorkspaceID, 32) || !key.MatchString(s.Key) || !key.MatchString(s.TransferID) || !hexadecimal(s.ApprovalDigest, 64) || s.HostID == d.HostID || s.StateID == d.StateID || s.WorkspaceID == d.WorkspaceID || !name.MatchString(a.CheckpointRepository) || !model.ValidCommit(a.Seal) || strings.ToLower(a.Seal) != a.Seal || !hexadecimal(a.ManifestDigest, 64) || len(a.Members) < 1 || len(a.Members) > 32 {
		return invalid
	}
	found := false
	for i, m := range a.Members {
		if !name.MatchString(m.Name) || i > 0 && a.Members[i-1].Name >= m.Name || m.Repository == "" || len(m.Repository) > 8192 || !utf8.ValidString(m.Repository) || strings.ContainsAny(m.Repository, "\x00\r\n\t") || !model.ValidBranchRef("refs/heads/"+m.Branch) || !model.ValidBranchRef("refs/heads/"+m.IntegrationBase) || !model.ValidCommit(m.Commit) || strings.ToLower(m.Commit) != m.Commit {
			return invalid
		}
		for _, previous := range a.Members[:i] {
			if previous.Repository == m.Repository && (previous.Branch == m.Branch || strings.HasPrefix(previous.Branch, m.Branch+"/") || strings.HasPrefix(m.Branch, previous.Branch+"/")) {
				return invalid
			}
		}
		for _, r := range m.Repository {
			if r < 32 || r == 127 {
				return invalid
			}
		}
		found = found || m.Name == a.CheckpointRepository
	}
	if !found {
		return invalid
	}
	expected, err := digest(a)
	if err != nil || !hexadecimal(a.Digest, 64) || a.Digest != expected {
		return invalid
	}
	return nil
}

// Seal creates a bounded, integrity-bound representation of already selected
// facts. It does not inspect a source host, retire a source or authorize import.
func Seal(a Artifact) ([]byte, error) {
	var err error
	a.Digest, err = digest(a)
	if err != nil {
		return nil, err
	}
	if err = a.validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	if len(raw)+1 > MaxArtifact {
		return nil, errors.New("cross-host handoff artifact exceeds its bound")
	}
	return append(raw, '\n'), nil
}

// Parse rejects unknown/duplicate fields, reordered fields, trailing values and
// altered vectors. An internally consistent digest is never a fencing proof.
func Parse(raw []byte) (Artifact, error) {
	var a Artifact
	invalid := errors.New("invalid cross-host handoff artifact encoding")
	if len(raw) > MaxArtifact || !utf8.Valid(raw) {
		return a, invalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&a); err != nil {
		return Artifact{}, invalid
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return Artifact{}, invalid
	}
	canonical, err := json.Marshal(a)
	if err != nil || !bytes.Equal(compact.Bytes(), canonical) {
		return Artifact{}, invalid
	}
	if err = a.validate(); err != nil {
		return Artifact{}, err
	}
	return a, nil
}

// RequireDestination binds consumption to actual local observations selected by
// the import coordinator. The caller must independently retain import intent
// and prove source retirement; this check alone grants no execution authority.
func (a Artifact) RequireDestination(actual Destination) error {
	if err := a.validate(); err != nil {
		return err
	}
	if !destination(actual) || a.Destination != actual {
		return errors.New("cross-host handoff belongs to another destination")
	}
	return nil
}
