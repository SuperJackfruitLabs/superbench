// Package payloadbootstrap owns fresh-only registered payload materialization.
// It never adopts a pre-existing checkout or treats a failed conversion as ready.
package payloadbootstrap

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceplan"
)

const Version = "superbench.payload-bootstrap.v1"
const MaxSpec = 64 << 10

var ErrBootstrap = errors.New("fresh registered payload bootstrap refused")
var name = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)
var id = regexp.MustCompile(`^[a-f0-9]{32}$`)
var sha1Pattern = regexp.MustCompile(`^[a-f0-9]{40}$`)
var sha256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Member struct{ Name, Repository, Branch, Source, ObjectFormat string }
type Spec struct {
	Version, Key, ID, CheckpointRepository string
	QuotaBytes                             uint64
	Members                                []Member
}

func (s Spec) Validate() error {
	if s.Version != Version || !name.MatchString(s.Key) || !id.MatchString(s.ID) || len(s.Members) == 0 || len(s.Members) > 32 || s.QuotaBytes > 1<<50 || s.QuotaBytes != 0 && (s.QuotaBytes < 1024 || s.QuotaBytes%1024 != 0) {
		return ErrBootstrap
	}
	last := ""
	for i, m := range s.Members {
		if !name.MatchString(m.Name) || i > 0 && m.Name <= last || !utf8.ValidString(m.Repository) || strings.ContainsRune(m.Repository, 0) || len(m.Repository) > 4096 || m.Repository == "" || !validBranch(m.Branch) || !(m.ObjectFormat == "sha1" && sha1Pattern.MatchString(m.Source) || m.ObjectFormat == "sha256" && sha256Pattern.MatchString(m.Source)) {
			return ErrBootstrap
		}
		last = m.Name
	}
	normalized := workspaceplan.Spec{Key: s.Key, CheckpointRepository: s.CheckpointRepository, QuotaBytes: s.QuotaBytes}
	for _, m := range s.Members {
		normalized.Repositories = append(normalized.Repositories, workspaceplan.Repository{Name: m.Name, Repository: m.Repository, Branch: m.Branch, Base: "main"})
	}
	if _, err := workspaceplan.Normalize(normalized); err != nil {
		return errors.Join(ErrBootstrap, err)
	}
	raw, err := json.Marshal(s)
	if err != nil || len(raw) > MaxSpec {
		return ErrBootstrap
	}
	return nil
}
func validBranch(s string) bool {
	return s != "" && len(s) <= 512 && utf8.ValidString(s) && !strings.HasPrefix(s, "-") && !strings.ContainsAny(s, "\x00\n\r ~^:?*[\\") && !strings.Contains(s, "..") && !strings.Contains(s, "@{") && !strings.HasPrefix(s, "/") && !strings.HasSuffix(s, "/") && !strings.Contains(s, "//") && !strings.HasSuffix(s, ".") && !strings.HasSuffix(s, ".lock")
}
func (s Spec) Bytes() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(s)
	if err != nil || len(raw) > MaxSpec {
		return nil, ErrBootstrap
	}
	return raw, nil
}
func ParseSpec(raw []byte) (Spec, error) {
	var s Spec
	if len(raw) > MaxSpec || !utf8.Valid(raw) || json.Unmarshal(raw, &s) != nil {
		return Spec{}, ErrBootstrap
	}
	expected, err := s.Bytes()
	var compact bytes.Buffer
	if err != nil || json.Compact(&compact, raw) != nil || !bytes.Equal(expected, compact.Bytes()) {
		return Spec{}, ErrBootstrap
	}
	return s, nil
}
