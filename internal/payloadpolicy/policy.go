// Package payloadpolicy owns the pure fixed payload policy and canonical codec.
package payloadpolicy

import (
	"errors"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const LayoutVersion = "superbench.payload-layout.v1"

var ErrPolicy = errors.New("invalid managed payload policy")

// Policy is supplied by the operator through the helper's fixed policy file.
// Requests cannot select credentials or replace this policy.
type Policy struct {
	DaemonUID, DaemonGID, PayloadUID, PayloadGID uint32
	DataRoot, StateRoot, CgroupRoot              string
}

func (p Policy) Validate() error {
	for _, id := range []uint32{p.DaemonUID, p.DaemonGID, p.PayloadUID, p.PayloadGID} {
		if id == 0 || id == ^uint32(0) {
			return ErrPolicy
		}
	}
	if p.DaemonUID == p.PayloadUID || p.DaemonGID == p.PayloadGID {
		return ErrPolicy
	}
	for _, path := range []string{p.DataRoot, p.StateRoot, p.CgroupRoot} {
		if !Directory(path) {
			return ErrPolicy
		}
	}
	for _, pair := range [][2]string{{p.DataRoot, p.StateRoot}, {p.StateRoot, p.DataRoot}} {
		rel, err := filepath.Rel(pair[0], pair[1])
		if err != nil || rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return ErrPolicy
		}
	}
	return nil
}

// Directory reports the existing normalized, bounded absolute directory syntax.
func Directory(path string) bool {
	return len(path) <= 4096 && utf8.ValidString(path) && !strings.ContainsRune(path, 0) && filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/"
}
