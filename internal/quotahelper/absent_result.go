package quotahelper

import (
	"bytes"
	"encoding/json"
	"errors"
	"unicode/utf8"
)

var errAbsent = errors.New("fixed-helper absent-create reply refused")

const AbsentVersion = "superbench.payload-absent-result.v1"

// AbsentResult is the fixed helper's read-only reply that root holds nothing
// of a create that never completed: no bootstrap intent, no allocation of its
// own and no leaf, for the workspace and generation it inspected. It is root's
// evidence for the daemon's record, never authority for an effect; the
// replacing create's bootstrap allocates through the ordinary checks.
type AbsentResult struct {
	Version, Key, ID string
	Generation       uint32
}

// Validate binds the reply to the workspace the daemon asked about.
func (r AbsentResult) Validate(key, id string, generation uint32) error {
	if r.Version != AbsentVersion || !reclaimKey.MatchString(key) || r.Key != key || !workspaceID.MatchString(id) || r.ID != id || r.Generation != generation {
		return errAbsent
	}
	return nil
}

// ParseAbsentResult accepts only the bounded canonical reply for the workspace.
func ParseAbsentResult(raw []byte, key, id string, generation uint32) (AbsentResult, error) {
	var r AbsentResult
	if len(raw) == 0 || len(raw) > MaxReply || !utf8.Valid(raw) || json.Unmarshal(raw, &r) != nil || r.Validate(key, id, generation) != nil {
		return AbsentResult{}, errAbsent
	}
	canonical, err := json.Marshal(r)
	var compact bytes.Buffer
	if err != nil || json.Compact(&compact, raw) != nil || !bytes.Equal(compact.Bytes(), canonical) {
		return AbsentResult{}, errAbsent
	}
	return r, nil
}
