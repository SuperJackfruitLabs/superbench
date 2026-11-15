package benchstate

import (
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
)

func aggregate() *model.Bench {
	data := "/srv/data"
	root := filepath.Join(data, "benches", "card")
	repo := "/srv/source.git"
	return &model.Bench{Key: "card", Path: root, Lease: &model.Lease{Holder: "initialization", Token: strings.Repeat("a", 48), Continuation: strings.Repeat("b", 32)}, Workspace: &model.Workspace{ID: strings.Repeat("c", 32), CreationID: "request1", Phase: "materializing", CheckpointRepository: "api", Members: []model.WorkspaceMember{{Name: "api", Repository: repo, Branch: "bench/card", Base: "main", Path: filepath.Join(root, "api"), Mirror: repository.Path(data, repo), Source: strings.Repeat("d", 40), ObjectFormat: "sha1"}}}}
}

func TestCaptureFreezesWorkspaceMetadataAcrossInPlaceMutations(t *testing.T) {
	changes := map[string]func(*model.Bench){
		"id":            func(b *model.Bench) { b.Workspace.ID = strings.Repeat("e", 32) },
		"creation":      func(b *model.Bench) { b.Workspace.CreationID = "request2" },
		"coordinator":   func(b *model.Bench) { b.Workspace.CheckpointRepository = "other" },
		"explicit":      func(b *model.Bench) { b.Workspace.ExplicitQuotaBytes = 4096 },
		"source":        func(b *model.Bench) { b.Workspace.Members[0].Source = strings.Repeat("e", 40) },
		"branch":        func(b *model.Bench) { b.Workspace.Members[0].Branch = "other" },
		"member-layout": func(b *model.Bench) { b.Workspace.Members[0].Path = "/srv/other" },
		"root":          func(b *model.Bench) { b.Path = "/srv/other" },
		"requested":     func(b *model.Bench) { b.RequestedQuotaBytes = 4096 },
		"token":         func(b *model.Bench) { b.Lease.Token = strings.Repeat("f", 48) },
		"continuation":  func(b *model.Bench) { b.Lease.Continuation = strings.Repeat("f", 32) },
		"kind":          func(b *model.Bench) { b.Workspace = nil },
		"lease":         func(b *model.Bench) { b.Lease = nil },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			records := Database{"card": aggregate()}
			snapshot := Capture(records)
			change(records["card"])
			if err := ValidateTransition(snapshot, records); err == nil {
				t.Fatal("immutable metadata mutation accepted")
			}
		})
	}
	records := Database{"card": aggregate()}
	snapshot := Capture(records)
	delete(records, "card")
	if err := ValidateTransition(snapshot, records); err == nil {
		t.Fatal("aggregate removal accepted")
	}
}

func TestTransitionAllowsForwardPhaseAndOneAllocation(t *testing.T) {
	records := Database{"card": aggregate()}
	before := Capture(records)
	b := records["card"]
	b.ProjectID = 17
	b.QuotaBytes = 4096
	b.QuotaInodes = 128
	b.Workspace.Phase = "initializing"
	if err := Validate(records); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTransition(before, records); err != nil {
		t.Fatal(err)
	}
	before = Capture(records)
	for _, change := range []func(*model.Bench){
		func(b *model.Bench) { b.ProjectID = 0 }, func(b *model.Bench) { b.ProjectID++ }, func(b *model.Bench) { b.QuotaBytes = 8192 }, func(b *model.Bench) { b.QuotaInodes++ }, func(b *model.Bench) { b.Workspace.Phase = "materializing" },
	} {
		copy := *b
		w := *b.Workspace
		copy.Workspace = &w
		change(&copy)
		if err := ValidateTransition(before, Database{"card": &copy}); err == nil {
			t.Fatal("assigned allocation/backward phase mutation accepted")
		}
	}
	if err := ValidateTransition(before, records); err != nil {
		t.Fatal(err)
	}
}

func TestScalarTransitionsRemainUnrestrictedButCannotConvertKind(t *testing.T) {
	records := Database{"card": {Key: "card", Repo: "/srv/repo", Path: "/srv/path", Mirror: "/srv/mirror"}}
	before := Capture(records)
	records["card"].Repo = "/srv/changed"
	records["card"].ProjectID = 12
	if err := ValidateTransition(before, records); err != nil {
		t.Fatal(err)
	}
	records["card"] = aggregate()
	if err := ValidateTransition(before, records); err == nil {
		t.Fatal("scalar kind conversion accepted")
	}
	if err := ValidateTransition(before, Database{}); err != nil {
		t.Fatal("legacy removal changed", err)
	}
	if err := ValidateTransition(Capture(Database{}), Database{"card": aggregate()}); err != nil {
		t.Fatal("new aggregate rejected", err)
	}
}
