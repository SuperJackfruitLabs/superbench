package workspacepolicy

import (
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
)

func recoveredRecord() *model.Bench {
	b := validRecord()
	b.Workspace.RecoveredFrom = &model.WorkspaceRecovery{Repository: b.Workspace.Members[0].Repository, BenchID: strings.Repeat("1", 32), Commit: strings.Repeat("2", 40), ManifestSHA256: strings.Repeat("3", 64)}
	return b
}
func TestRecoveryProvenanceUsesAllowlistedMetadataRepositoryFormat(t *testing.T) {
	b := recoveredRecord()
	if err := Validate(b); err != nil {
		t.Fatal(err)
	}
	for _, historical := range []func(*model.Bench) error{ValidateSchema14, ValidateSchema15, ValidateSchema16} {
		if err := historical(b); err == nil {
			t.Fatal("new provenance accepted by historical schema")
		}
	}
	// Mixed object formats remain valid; the source seal belongs to source.Repo,
	// independently of which allowed member coordinates the new workspace.
	data := filepath.Dir(filepath.Dir(b.Path))
	repo := "/srv/second.git"
	b.Workspace.Members = append(b.Workspace.Members, model.WorkspaceMember{Name: "web", Repository: repo, Branch: "bench/card", Base: "main", Path: filepath.Join(b.Path, "web"), Mirror: repository.Path(data, repo), Source: strings.Repeat("4", 64), ObjectFormat: "sha256", RequestedQuotaBytes: 4096})
	b.Workspace.CheckpointRepository = "web"
	if err := Validate(b); err != nil {
		t.Fatal(err)
	}
	b.Workspace.RecoveredFrom.Repository = repo
	b.Workspace.RecoveredFrom.Commit = strings.Repeat("2", 64)
	if err := Validate(b); err != nil {
		t.Fatal(err)
	}
	b.Workspace.RecoveredFrom.Commit = strings.Repeat("2", 40)
	if err := Validate(b); err == nil {
		t.Fatal("source seal width differs from its allowlisted repository")
	}
}
func TestRecoveryProvenanceRejectsUnregisteredIdentityAndSelfRecovery(t *testing.T) {
	mutations := map[string]func(*model.Bench){
		"repo":         func(b *model.Bench) { b.Workspace.RecoveredFrom.Repository = "/srv/unregistered.git" },
		"source-id":    func(b *model.Bench) { b.Workspace.RecoveredFrom.BenchID = "bad" },
		"same-id":      func(b *model.Bench) { b.Workspace.RecoveredFrom.BenchID = b.Workspace.ID },
		"commit-width": func(b *model.Bench) { b.Workspace.RecoveredFrom.Commit = strings.Repeat("2", 64) },
		"commit-case":  func(b *model.Bench) { b.Workspace.RecoveredFrom.Commit = strings.Repeat("A", 40) },
		"digest":       func(b *model.Bench) { b.Workspace.RecoveredFrom.ManifestSHA256 = strings.Repeat("z", 64) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			b := recoveredRecord()
			mutate(b)
			if err := Validate(b); err == nil {
				t.Fatal("invalid recovery provenance accepted")
			}
		})
	}
}
