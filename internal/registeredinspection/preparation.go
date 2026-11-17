package registeredinspection

import (
	"bytes"
	"encoding/json"
	"regexp"
	"unicode/utf8"
)

const PreparationVersion = "superbench.preparation-inspection.v1"
const MaxPreparationRequest = 1024
const MaxPreparation = 256 << 10

var layoutIdentity = regexp.MustCompile(`^[a-f0-9]{32}$`)

// PreparationRequest selects one registered member's fixed inputs. It grants
// neither execution nor writer quiescence and carries no caller-selected path.
type PreparationRequest struct{ Version, Key, LayoutID, PolicyDigest, Member string }

func (r PreparationRequest) Validate() error {
	if r.Version != PreparationVersion || !memberName.MatchString(r.Key) || !layoutIdentity.MatchString(r.LayoutID) || !lowerHash.MatchString(r.PolicyDigest) || !memberName.MatchString(r.Member) {
		return ErrBoundary
	}
	return nil
}
func (r PreparationRequest) Bytes() ([]byte, error) {
	if r.Validate() != nil {
		return nil, ErrBoundary
	}
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > MaxPreparationRequest {
		return nil, ErrBoundary
	}
	return raw, nil
}
func ParsePreparationRequest(raw []byte) (PreparationRequest, error) {
	var r PreparationRequest
	if len(raw) > MaxPreparationRequest || !utf8.Valid(raw) || json.Unmarshal(raw, &r) != nil {
		return PreparationRequest{}, ErrBoundary
	}
	canonical, err := r.Bytes()
	var compact bytes.Buffer
	if err != nil || json.Compact(&compact, raw) != nil || !bytes.Equal(compact.Bytes(), canonical) {
		return PreparationRequest{}, ErrBoundary
	}
	return r, nil
}

// Preparation contains private read observations only. The daemon must parse
// Inputs.Config using setup, and verify repository identity through typed Git.
// Unregistered reports only whether the aggregate root holds any entry outside
// the registered layout; no name or content of such an entry crosses.
type Preparation struct {
	Version      string
	Layout       Layout
	Inputs       Inputs
	Unregistered bool `json:",omitempty"`
}

func (v Preparation) Bytes(p Policy) ([]byte, error) {
	if v.Version != PreparationVersion || v.Layout.Validate(p) != nil || v.Layout.Scalar || v.Inputs.Validate() != nil {
		return nil, ErrBoundary
	}
	found := false
	for _, m := range v.Layout.Members {
		found = found || m.Name == v.Inputs.Member
	}
	if !found {
		return nil, ErrBoundary
	}
	raw, err := json.Marshal(v)
	if err != nil || len(raw) > MaxPreparation {
		return nil, ErrBoundary
	}
	return raw, nil
}
func ParsePreparation(p Policy, raw []byte) (Preparation, error) {
	var v Preparation
	if len(raw) > MaxPreparation || !utf8.Valid(raw) || json.Unmarshal(raw, &v) != nil {
		return Preparation{}, ErrBoundary
	}
	canonical, err := v.Bytes(p)
	var compact bytes.Buffer
	if err != nil || json.Compact(&compact, raw) != nil || !bytes.Equal(compact.Bytes(), canonical) {
		return Preparation{}, ErrBoundary
	}
	return v, nil
}
