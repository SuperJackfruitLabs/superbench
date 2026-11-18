package quotahelper

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"unicode/utf8"
)

var errRetired = errors.New("fixed-helper retired-create reply refused")

const RetiredVersion = "superbench.payload-retired-result.v1"

// RetiredResult is the fixed helper's read-only reply that root retired the
// allocation of a create that never completed: the workspace and generation
// it inspected and the allocation's permanent zero tombstone. It is root's
// evidence for the daemon's record, never authority for an effect; the next
// generation's bootstrap checks the same evidence again.
type RetiredResult struct {
	Version, Key, ID string
	Generation       uint32
	Allocation       Assignment
}

// Validate binds the reply to the workspace the daemon asked about.
func (r RetiredResult) Validate(data, key, id string, generation uint32) error {
	a := r.Allocation
	if r.Version != RetiredVersion || !reclaimKey.MatchString(key) || r.Key != key || !workspaceID.MatchString(id) || r.ID != id || r.Generation != generation ||
		a.Path != filepath.Join(data, "benches", key) || a.ProjectID < 10000 || a.Bytes != 0 || a.Inodes != 0 {
		return errRetired
	}
	return nil
}

// ParseRetiredResult accepts only the bounded canonical reply for the workspace.
func ParseRetiredResult(raw []byte, data, key, id string, generation uint32) (RetiredResult, error) {
	var r RetiredResult
	if len(raw) == 0 || len(raw) > MaxReply || !utf8.Valid(raw) || json.Unmarshal(raw, &r) != nil || r.Validate(data, key, id, generation) != nil {
		return RetiredResult{}, errRetired
	}
	canonical, err := json.Marshal(r)
	var compact bytes.Buffer
	if err != nil || json.Compact(&compact, raw) != nil || !bytes.Equal(compact.Bytes(), canonical) {
		return RetiredResult{}, errRetired
	}
	return r, nil
}
