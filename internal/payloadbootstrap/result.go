package payloadbootstrap

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
)

const ResultVersion = "superbench.payload-bootstrap-result.v1"

// Result binds a completed grouped registration to the actual fresh allocation.
// It is helper evidence, never caller authority to adopt or reown a directory.
type Result struct {
	Version    string
	Layout     payloadexec.Layout
	Allocation quotahelper.Assignment
}

func (r Result) Validate(p payloadexec.Policy, s Spec) error {
	if s.Validate() != nil || r.Version != ResultVersion || r.Layout.Validate(p) != nil || r.Layout.Scalar || r.Layout.Key != s.Key || r.Layout.ID != s.ID || len(r.Layout.Members) != len(s.Members) {
		return ErrBootstrap
	}
	for j, member := range s.Members {
		if r.Layout.Members[j].Name != member.Name {
			return ErrBootstrap
		}
	}
	a := r.Allocation
	if a.Path != filepath.Join(p.DataRoot, "benches", s.Key) || a.ProjectID < 10000 || a.Bytes < 1024 || a.Bytes > 1<<50 || a.Bytes%1024 != 0 || a.Inodes == 0 || a.Inodes > 1<<50 || s.QuotaBytes != 0 && a.Bytes != s.QuotaBytes {
		return ErrBootstrap
	}
	return nil
}

func ParseResult(raw []byte, p payloadexec.Policy, s Spec) (Result, error) {
	var r Result
	if len(raw) == 0 || len(raw) > quotahelper.MaxReply || !utf8.Valid(raw) || json.Unmarshal(raw, &r) != nil || r.Validate(p, s) != nil {
		return Result{}, ErrBootstrap
	}
	canonical, err := json.Marshal(r)
	var compact bytes.Buffer
	if err != nil || json.Compact(&compact, raw) != nil || !bytes.Equal(compact.Bytes(), canonical) {
		return Result{}, ErrBootstrap
	}
	return r, nil
}
