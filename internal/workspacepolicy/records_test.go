package workspacepolicy

import (
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
)

func validRecord() *model.Bench {
	data := "/srv/bench-data"
	root := filepath.Join(data, "benches", "card")
	repo := "/srv/source.git"
	return &model.Bench{Key: "card", Path: root, RequestedQuotaBytes: 4096, Lease: &model.Lease{Holder: "initialization", Token: strings.Repeat("a", 48), Continuation: strings.Repeat("b", 32)}, Workspace: &model.Workspace{ID: strings.Repeat("c", 32), CreationID: "request1", CheckpointRepository: "api", Phase: "materializing", ExplicitQuotaBytes: 4096, Members: []model.WorkspaceMember{{Name: "api", Repository: repo, Branch: "bench/card", Base: "main", Path: filepath.Join(root, "api"), Mirror: repository.Path(data, repo), Source: strings.Repeat("d", 40), ObjectFormat: "sha1", RequestedQuotaBytes: 4096}}}}
}

func TestValidateInitializationRecord(t *testing.T) {
	for _, assigned := range []bool{false, true} {
		b := validRecord()
		if assigned {
			b.ProjectID = 10
			b.QuotaBytes = 4096
			b.QuotaInodes = 128
			b.Workspace.Phase = "initializing"
		}
		if err := Validate(b); err != nil {
			t.Fatal(err)
		}
	}
	b := validRecord()
	b.RequestedQuotaBytes = 0
	b.Workspace.ExplicitQuotaBytes = 0
	b.Workspace.Members[0].RequestedQuotaBytes = 0
	b.Lease.Token = strings.Repeat("e", 32)
	if err := Validate(b); err != nil {
		t.Fatal(err)
	}
}

func TestRejectInvalidAggregateWithoutMutation(t *testing.T) {
	tests := map[string]func(*model.Bench){
		"scalar-repo":         func(b *model.Bench) { b.Repo = "/srv/other" },
		"scalar-checkpoint":   func(b *model.Bench) { b.Checkpoint = strings.Repeat("d", 40) },
		"archived":            func(b *model.Bench) { b.ArchivedAt = "2026-11-15T00:00:00Z" },
		"archive-evidence":    func(b *model.Bench) { b.ArchiveMerge = &model.ArchiveMergeEvidence{} },
		"bad-key":             func(b *model.Bench) { b.Key = "../card" },
		"bad-root":            func(b *model.Bench) { b.Path = "/srv/other/card" },
		"bad-id":              func(b *model.Bench) { b.Workspace.ID = strings.Repeat("C", 32) },
		"bad-creation":        func(b *model.Bench) { b.Workspace.CreationID = "../request" },
		"ready":               func(b *model.Bench) { b.Workspace.Phase = "ready" },
		"no-lease":            func(b *model.Bench) { b.Lease = nil },
		"wrong-holder":        func(b *model.Bench) { b.Lease.Holder = "worker" },
		"bad-token":           func(b *model.Bench) { b.Lease.Token = strings.Repeat("g", 48) },
		"bad-continuation":    func(b *model.Bench) { b.Lease.Continuation = "" },
		"execution":           func(b *model.Bench) { b.Lease.Execution = &model.Execution{} },
		"retained":            func(b *model.Bench) { b.Lease.RetainedRuns = []string{strings.Repeat("1", 32)} },
		"stopped":             func(b *model.Bench) { b.Lease.StopRequested = true },
		"no-members":          func(b *model.Bench) { b.Workspace.Members = nil },
		"missing-coordinator": func(b *model.Bench) { b.Workspace.CheckpointRepository = "missing" },
		"duplicate":           func(b *model.Bench) { b.Workspace.Members = append(b.Workspace.Members, b.Workspace.Members[0]) },
		"member-path":         func(b *model.Bench) { b.Workspace.Members[0].Path = "/srv/other" },
		"member-mirror":       func(b *model.Bench) { b.Workspace.Members[0].Mirror = "/srv/other" },
		"remote-credentials": func(b *model.Bench) {
			m := &b.Workspace.Members[0]
			m.Repository = "https://user:pass@example.com/r"
			m.Mirror = repository.Path("/srv/bench-data", m.Repository)
		},
		"branch":                 func(b *model.Bench) { b.Workspace.Members[0].Branch = "refs/heads/card" },
		"base":                   func(b *model.Bench) { b.Workspace.Members[0].Base = "HEAD" },
		"source":                 func(b *model.Bench) { b.Workspace.Members[0].Source = strings.Repeat("e", 64) },
		"quota-source":           func(b *model.Bench) { b.Workspace.Members[0].RequestedQuotaBytes = 8192 },
		"quota-explicit":         func(b *model.Bench) { b.Workspace.ExplicitQuotaBytes = 8192 },
		"quota-budget":           func(b *model.Bench) { b.RequestedQuotaBytes = 1 },
		"allocation-unassigned":  func(b *model.Bench) { b.QuotaBytes = 4096 },
		"allocation-wrong-cap":   func(b *model.Bench) { b.ProjectID = 1; b.QuotaBytes = 8192; b.QuotaInodes = 1 },
		"allocation-no-inodes":   func(b *model.Bench) { b.ProjectID = 1; b.QuotaBytes = 4096 },
		"allocation-huge-inodes": func(b *model.Bench) { b.ProjectID = 1; b.QuotaBytes = 4096; b.QuotaInodes = (1 << 40) + 1 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			b := validRecord()
			mutate(b)
			if err := Validate(b); err == nil {
				t.Fatal("invalid aggregate accepted")
			}
		})
	}
}
