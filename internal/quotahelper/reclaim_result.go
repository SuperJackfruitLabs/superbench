package quotahelper

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"unicode/utf8"
)

var reclaimKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)

var errReclaim = errors.New("fixed-helper reclaim reply refused")

const ReclaimVersion = "superbench.payload-reclaim-result.v1"

// ReclaimResult is the fixed helper's reply to a completed reclaim, and to its
// exact retry: the bench's allocation as its permanent zero tombstone. It is
// evidence the daemon checks against its own record, never authority.
type ReclaimResult struct {
	Version, Key string
	Allocation   Assignment
}

// Validate binds the tombstone to the bench the daemon asked to reclaim.
func (r ReclaimResult) Validate(data, key string, project uint32) error {
	a := r.Allocation
	if r.Version != ReclaimVersion || !reclaimKey.MatchString(key) || r.Key != key || a.Path != filepath.Join(data, "benches", key) || project < 10000 || a.ProjectID != project || a.Bytes != 0 || a.Inodes != 0 {
		return errReclaim
	}
	return nil
}

// ParseReclaimResult accepts only the bounded canonical reply for key.
func ParseReclaimResult(raw []byte, data, key string, project uint32) (ReclaimResult, error) {
	var r ReclaimResult
	if len(raw) == 0 || len(raw) > MaxReply || !utf8.Valid(raw) || json.Unmarshal(raw, &r) != nil || r.Validate(data, key, project) != nil {
		return ReclaimResult{}, errReclaim
	}
	canonical, err := json.Marshal(r)
	var compact bytes.Buffer
	if err != nil || json.Compact(&compact, raw) != nil || !bytes.Equal(compact.Bytes(), canonical) {
		return ReclaimResult{}, errReclaim
	}
	return r, nil
}
