// Package payloadexec owns the managed payload identity boundary. It is separate
// from daemon Git supervision and is not enabled without its fixed host policy.
package payloadexec

import (
	"errors"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

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
		if !directory(path) {
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

func directory(path string) bool {
	return len(path) <= 4096 && utf8.ValidString(path) && !strings.ContainsRune(path, 0) && filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/"
}

// Environment never inherits daemon credentials, loader hooks, shell startup
// programs, sudo metadata or daemon Git configuration. HOME and TMPDIR must come
// from the validated private payload layout, not the ambient environment.
func Environment(home, tmp string, ambient []string) ([]string, error) {
	if !directory(home) || !directory(tmp) || home == tmp {
		return nil, ErrPolicy
	}
	result := []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + home, "TMPDIR=" + tmp, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
	allowed := map[string]bool{"LANG": true, "LC_ALL": true, "LC_CTYPE": true, "TZ": true, "TERM": true, "CI": true}
	seen := map[string]bool{}
	for _, item := range ambient {
		key, value, ok := strings.Cut(item, "=")
		if !allowed[key] {
			continue
		}
		if !ok || seen[key] || len(value) > 256 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return nil, ErrPolicy
		}
		seen[key] = true
		result = append(result, item)
	}
	return result, nil
}
