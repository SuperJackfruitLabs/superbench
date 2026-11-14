package service

import (
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"net/url"
	"strings"
)

type executionDTO struct {
	ID       string `json:"id"`
	Phase    string `json:"phase"`
	LogPath  string `json:"log_path"`
	LogLimit uint64 `json:"log_limit_bytes"`
}
type leaseDTO struct {
	Mode      string        `json:"mode"`
	Holder    string        `json:"holder"`
	Token     string        `json:"token,omitempty"`
	Execution *executionDTO `json:"execution,omitempty"`
}
type benchDTO struct {
	Key         string    `json:"key"`
	Repository  string    `json:"repository"`
	Branch      string    `json:"branch"`
	Path        string    `json:"path"`
	Checkpoint  string    `json:"checkpoint"`
	ArchivedAt  string    `json:"archived_at,omitempty"`
	Reaping     bool      `json:"reaping,omitempty"`
	ReapedAt    string    `json:"reaped_at,omitempty"`
	ProjectID   uint32    `json:"project_id"`
	QuotaInodes uint64    `json:"quota_inodes"`
	QuotaBytes  uint64    `json:"quota_bytes"`
	Lease       *leaseDTO `json:"lease,omitempty"`
}

func executionView(e *bench.Execution) *executionDTO {
	if e == nil {
		return nil
	}
	return &executionDTO{ID: e.ID, Phase: e.Phase, LogPath: e.Log, LogLimit: e.LogLimitBytes}
}
func benchView(b *bench.Bench, token bool) *benchDTO {
	if b == nil {
		return nil
	}
	v := &benchDTO{Key: b.Key, Repository: displayRepository(b.Repo), Branch: b.Branch, Path: b.Path, Checkpoint: b.Checkpoint, ProjectID: b.ProjectID, QuotaBytes: b.QuotaBytes, QuotaInodes: b.QuotaInodes, ArchivedAt: b.ArchivedAt, Reaping: b.ReapingDigest != "" && b.ReapedAt == "", ReapedAt: b.ReapedAt}
	if b.Lease != nil {
		mode := "cooperative"
		if b.Lease.Execution != nil {
			mode = "supervised"
		}
		v.Lease = &leaseDTO{Mode: mode, Holder: b.Lease.Holder, Execution: executionView(b.Lease.Execution)}
		if token {
			v.Lease.Token = b.Lease.Token
		}
	}
	return v
}
func runView(r *bench.RunResult) any {
	if r == nil {
		return nil
	}
	return map[string]any{"bench": benchView(r.Bench, true), "execution": executionView(r.Execution), "exit_code": r.ExitCode, "log_truncated": r.LogTruncated}
}

func displayRepository(repository string) string {
	if !strings.Contains(repository, "://") {
		return repository
	}
	u, e := url.Parse(repository)
	if e != nil {
		return "[repository unavailable]"
	}
	if u.User != nil {
		u.User = url.User(u.User.Username())
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
