package workspacepolicy

import (
	"encoding/json"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/checkpointset"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func archiveRecord(t *testing.T, automatic bool) *model.Bench {
	t.Helper()
	b := prepared()
	b.Workspace.Phase = "ready"
	b.Lease = nil
	b.Workspace.Checkpoint = checkpoint(t, b)
	b.Workspace.Checkpoint.Seal = strings.Repeat("f", 40)
	if automatic {
		b.Workspace.Checkpoint.Members[0].Commit = strings.Repeat("9", 40)
		c := b.Workspace.Checkpoint
		b.Workspace.Checkpoint = nil
		digest := checkpoint(t, b)
		digest.Members = c.Members
		m, err := Manifest(b, digest)
		if err != nil {
			t.Fatal(err)
		}
		digest.Digest = canonicalDigest(t, m)
		digest.Seal = c.Seal
		b.Workspace.Checkpoint = digest
		b.Workspace.Unmerged = []model.WorkspaceMergeEvidence{{Name: "api", Evidence: model.ArchiveMergeEvidence{Checkpoint: digest.Members[0].Commit, ConfigDigest: strings.Repeat("a", 64), TargetBranch: "refs/heads/main", TargetCommit: strings.Repeat("b", 40), ObservedAt: "2030-01-01T00:00:00Z"}}}
	}
	b.Workspace.Phase = "archived"
	b.Workspace.Archive = &model.WorkspaceArchive{ArchivedAt: "2030-01-01T02:00:00Z", ReapAfter: "2030-01-08T02:00:00Z", FreezeDigest: strings.Repeat("c", 64)}
	if automatic {
		p := b.Workspace.Unmerged[0]
		p.Evidence.ObservedAt = "2030-01-01T01:00:00Z"
		b.Workspace.Archive.Merge = []model.WorkspaceMergeEvidence{p}
	}
	return b
}
func TestAggregateArchiveAndReapIntentStates(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		b := archiveRecord(t, automatic)
		if err := Validate(b); err != nil {
			t.Fatal(err)
		}
		b.Workspace.Archive.FrozenAt = "2030-01-01T02:01:00Z"
		if err := Validate(b); err != nil {
			t.Fatal(err)
		}
		b.Workspace.Phase = "reaping"
		b.Workspace.Archive.ReapingDigest = strings.Repeat("d", 64)
		if err := Validate(b); err != nil {
			t.Fatal(err)
		}
		b.Workspace.Phase = "reaped"
		b.Workspace.Archive.ReapedAt = "2030-01-08T03:00:00Z"
		if err := Validate(b); err != nil {
			t.Fatal(err)
		}
		for _, historical := range []func(*model.Bench) error{ValidateSchema14, ValidateSchema15, ValidateSchema16, ValidateSchema17} {
			if err := historical(b); err == nil {
				t.Fatal("historical schema adopted archive lifecycle")
			}
		}
	}
}
func TestAggregateArchiveRejectsBrokenEvidenceAndRetention(t *testing.T) {
	changes := map[string]func(*model.Bench){
		"no-checkpoint":    func(b *model.Bench) { b.Workspace.Checkpoint = nil },
		"malformed-vector": func(b *model.Bench) { b.Workspace.Checkpoint.Members = nil },
		"lease":            func(b *model.Bench) { b.Lease = &model.Lease{Holder: "worker", Token: strings.Repeat("1", 32)} },
		"stage": func(b *model.Bench) {
			b.Workspace.Stage = &model.WorkspaceStage{Name: "api", Digest: strings.Repeat("1", 64)}
		},
		"pending":          func(b *model.Bench) { b.Workspace.Pending = b.Workspace.Checkpoint },
		"bad-freeze":       func(b *model.Bench) { b.Workspace.Archive.FreezeDigest = "bad" },
		"missing-deadline": func(b *model.Bench) { b.Workspace.Archive.ReapAfter = "" },
		"deadline-before":  func(b *model.Bench) { b.Workspace.Archive.ReapAfter = "2030-01-01T01:00:00Z" },
		"excess-grace":     func(b *model.Bench) { b.Workspace.Archive.ReapAfter = "2032-01-01T02:00:00Z" },
		"early-freeze":     func(b *model.Bench) { b.Workspace.Archive.FrozenAt = "2030-01-01T01:00:00Z" },
		"reap-without-freeze": func(b *model.Bench) {
			b.Workspace.Phase = "reaping"
			b.Workspace.Archive.ReapingDigest = strings.Repeat("d", 64)
		},
		"unknown-member": func(b *model.Bench) { b.Workspace.Archive.Merge[0].Name = "missing" },
		"bad-config":     func(b *model.Bench) { b.Workspace.Archive.Merge[0].Evidence.ConfigDigest = "bad" },
		"wrong-commit":   func(b *model.Bench) { b.Workspace.Archive.Merge[0].Evidence.Checkpoint = strings.Repeat("8", 40) },
		"task-target": func(b *model.Bench) {
			b.Workspace.Archive.Merge[0].Evidence.TargetBranch = "refs/heads/" + b.Workspace.Members[0].Branch
		},
		"target-format":         func(b *model.Bench) { b.Workspace.Archive.Merge[0].Evidence.TargetCommit = strings.Repeat("8", 64) },
		"no-unmerged":           func(b *model.Bench) { b.Workspace.Unmerged = nil },
		"changed-config":        func(b *model.Bench) { b.Workspace.Archive.Merge[0].Evidence.ConfigDigest = strings.Repeat("8", 64) },
		"reordered-observation": func(b *model.Bench) { b.Workspace.Unmerged[0].Evidence.ObservedAt = "2030-01-01T01:30:00Z" },
		"merge-after-intent":    func(b *model.Bench) { b.Workspace.Archive.Merge[0].Evidence.ObservedAt = "2030-01-01T03:00:00Z" },
		"ready-with-archive":    func(b *model.Bench) { b.Workspace.Phase = "ready" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			b := archiveRecord(t, true)
			change(b)
			if err := Validate(b); err == nil {
				t.Fatal("invalid archive record accepted")
			}
		})
	}
	b := archiveRecord(t, false)
	b.Workspace.Phase = "reaped"
	b.Workspace.Archive.FrozenAt = "2030-01-01T02:01:00Z"
	b.Workspace.Archive.ReapingDigest = strings.Repeat("d", 64)
	b.Workspace.Archive.ReapedAt = "2030-01-02T00:00:00Z"
	if err := Validate(b); err == nil {
		t.Fatal("reaped before retention deadline")
	}
}

func canonicalDigest(t *testing.T, m checkpointset.Manifest) string {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return checkpointset.Digest(raw)
}

func TestAutomaticArchiveRejectsBaselineOnlyMergeEvidence(t *testing.T) {
	b := archiveRecord(t, false)
	if err := Validate(b); err != nil {
		t.Fatal("manual baseline archive rejected", err)
	}
	b.Workspace.Archive.Merge = []model.WorkspaceMergeEvidence{{Name: "api", Evidence: model.ArchiveMergeEvidence{
		Checkpoint: b.Workspace.Members[0].Source, ConfigDigest: strings.Repeat("a", 64),
		TargetBranch: "refs/heads/main", TargetCommit: strings.Repeat("b", 40), ObservedAt: "2030-01-01T01:00:00Z",
	}}}
	if err := Validate(b); err == nil {
		t.Fatal("baseline reachability fabricated automatic completion")
	}
}

func TestAutomaticArchiveCoversUnchangedMembersWithoutInventingUnmergedProof(t *testing.T) {
	b := archiveRecord(t, true)
	w := b.Workspace
	data := filepath.Dir(filepath.Dir(b.Path))
	repo := "/srv/web.git"
	member := model.WorkspaceMember{Name: "web", Repository: repo, Branch: "bench/card", Base: "main", Path: filepath.Join(b.Path, "web"), Mirror: repository.Path(data, repo), Source: strings.Repeat("4", 64), ObjectFormat: "sha256", RequestedQuotaBytes: 4096}
	w.Members = append(w.Members, member)
	w.Checkpoint.Members = append(w.Checkpoint.Members, model.WorkspaceCommit{Name: "web", Commit: member.Source})
	m, err := Manifest(b, w.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	w.Checkpoint.Digest = canonicalDigest(t, m)
	web := model.WorkspaceMergeEvidence{Name: "web", Evidence: model.ArchiveMergeEvidence{Checkpoint: member.Source, ConfigDigest: strings.Repeat("5", 64), TargetBranch: "refs/heads/main", TargetCommit: strings.Repeat("6", 64), ObservedAt: "2030-01-01T01:00:00Z"}}
	w.Archive.Merge = append(w.Archive.Merge, web)
	if err := Validate(b); err != nil {
		t.Fatal("unchanged member required a fabricated unmerged proof", err)
	}
	w.Archive.Merge = w.Archive.Merge[:1]
	if err := Validate(b); err == nil {
		t.Fatal("automatic archive accepted partial merge vector")
	}
	w.Archive.Merge = append(w.Archive.Merge, web)
	w.Unmerged = append(w.Unmerged, web)
	if err := Validate(b); err == nil {
		t.Fatal("unchanged member acquired fabricated unmerged proof")
	}
}
