package payloadexec

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"
)

// InspectionVersion is a distinct read-only helper action, not an execution
// kind. It never selects credentials, paths, a command or an execution scope.
const InspectionVersion = "superbench.payload-inspection.v1"
const MaxInspectionRequest = 1024

type InspectionRequest struct{ Version, Key, LayoutID, PolicyDigest string }

func (r InspectionRequest) Validate() error {
	if r.Version != InspectionVersion || !logical.MatchString(r.Key) || !identity.MatchString(r.LayoutID) || !hostIdentity.MatchString(r.PolicyDigest) {
		return ErrRequest
	}
	return nil
}
func (r InspectionRequest) Bytes() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > MaxInspectionRequest {
		return nil, ErrRequest
	}
	return raw, nil
}
func ParseInspectionRequest(raw []byte) (InspectionRequest, error) {
	var r InspectionRequest
	if len(raw) > MaxInspectionRequest || !utf8.Valid(raw) || json.Unmarshal(raw, &r) != nil {
		return InspectionRequest{}, ErrRequest
	}
	canonical, err := r.Bytes()
	var compact bytes.Buffer
	if err != nil || json.Compact(&compact, raw) != nil || !bytes.Equal(compact.Bytes(), canonical) {
		return InspectionRequest{}, ErrRequest
	}
	return r, nil
}
