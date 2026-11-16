package crosshostflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/crosshost"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hostidentity"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

func TestActualLocalOfferReservesCoreBeforeAnyOrdinaryCreationEffects(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	m := Manager{Store: f.dest}
	o, err := m.Offer(ctx, f.spec.Key, "import-create", 1<<20)
	must(t, err)
	host, err := hostidentity.Current(ctx)
	must(t, err)
	if o.Destination.HostID != host {
		t.Fatal("offer did not observe actual host")
	}
	replay, err := m.Offer(ctx, f.spec.Key, "import-create", 1<<20)
	must(t, err)
	if !reflect.DeepEqual(o, replay) {
		t.Fatal("offer replay generated another identity")
	}
	if _, err = m.Offer(ctx, f.spec.Key, "other-create", 1<<20); err == nil {
		t.Fatal("recycled reserved key")
	}
	if _, err = f.dest.StageWorkspaceContext(ctx, "ordinary", f.spec); err == nil {
		t.Fatal("ordinary aggregate stole reservation")
	}
	if _, err = f.dest.CreateColdContext(ctx, f.spec.Key, model.Options{Repo: f.spec.Repositories[0].Repository, Branch: "card/ordinary"}, false); err == nil {
		t.Fatal("ordinary scalar stole reservation")
	}
	if _, err = os.Lstat(filepath.Join(f.dest.Data, "benches", f.spec.Key)); !os.IsNotExist(err) {
		t.Fatal("ordinary creation made reserved root", err)
	}
	if _, err = os.Lstat(filepath.Join(f.dest.State, "create-"+f.spec.Key+".json")); !os.IsNotExist(err) {
		t.Fatal("ordinary creation made intent", err)
	}
}

// These are actual local-source export checks. A remote offer fixture identifies
// an unobserved second machine; this test does not claim a two-host runtime flow.
func TestActualSourceExportDetachesWholeVectorAndNeverRestoresWriter(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	dest := Manager{Store: f.dest}
	source := Manager{Store: f.source}
	o, err := dest.Offer(ctx, f.spec.Key, "import-create", 1<<20)
	must(t, err)
	if _, err = source.InspectExport(ctx, f.spec.Key, "export", *o); err == nil {
		t.Fatal("same actual host was accepted as cross-host")
	}
	o.Destination.HostID = strings.Repeat("e", 64)
	p, err := source.InspectExport(ctx, f.spec.Key, "export", *o)
	must(t, err)
	must(t, os.WriteFile(filepath.Join(f.ready.Workspace.Members[1].Path, "private-output"), []byte("after review"), 0600))
	if _, err = source.BeginExport(ctx, *p); err == nil {
		t.Fatal("stale later member permitted export")
	}
	if snapshot(t, f.source, f.spec.Key).Workspace.Transfer != nil {
		t.Fatal("stale approval marked source")
	}
	p, err = source.InspectExport(ctx, f.spec.Key, "export", *o)
	must(t, err)
	_, err = source.BeginExport(ctx, *p)
	must(t, err)
	if _, err = f.source.AcquireWorkspaceContext(ctx, f.spec.Key, "later"); err == nil {
		t.Fatal("source reacquired after export start")
	}
	a, err := source.ContinueExport(ctx, f.spec.Key, "export")
	must(t, err)
	for _, r := range f.ready.Workspace.Members {
		if git(t, r.Path, "rev-parse", "HEAD") != r.Source {
			t.Fatal("changed source content")
		}
		if git(t, r.Path, "rev-parse", "--abbrev-ref", "HEAD") != "HEAD" {
			t.Fatal("source still owns task branch")
		}
	}
	raw, err := os.ReadFile(filepath.Join(f.ready.Workspace.Members[1].Path, "private-output"))
	must(t, err)
	if string(raw) != "after review" {
		t.Fatal("lost ignored source work")
	}
	again, err := source.ContinueExport(ctx, f.spec.Key, "export")
	must(t, err)
	if !reflect.DeepEqual(a, again) {
		t.Fatal("terminal replay changed artifact")
	}
	// A later destination may advance the original refs. Source replay retains
	// immutable evidence without reacquiring or demanding ownership of those refs.
	for _, r := range f.ready.Workspace.Members {
		git(t, r.Repository, "commit", "--allow-empty", "-m", "later destination commit")
		git(t, r.Repository, "update-ref", "refs/heads/"+r.Branch, git(t, r.Repository, "rev-parse", "HEAD"))
	}
	advancedReplay, err := source.ContinueExport(ctx, f.spec.Key, "export")
	must(t, err)
	if !reflect.DeepEqual(a, advancedReplay) {
		t.Fatal("terminal export changed after legitimate task-ref advance")
	}

	p.Destination.Destination.Key = "other"
	p.Digest = ""
	p.Digest = hash(*p)
	if _, err = source.BeginExport(ctx, *p); err == nil {
		t.Fatal("terminal export redirected")
	}
}

// Destination tests deliberately use a reviewed artifact fixture with a remote
// host ID. They exercise real Git/SQLite/setup/genesis, not remote stop proof.
func incomingFixture(t *testing.T) (fixture, Manager, crosshost.Artifact) {
	f := setup(t)
	ctx := context.Background()
	m := Manager{Store: f.dest}
	o, err := m.Offer(ctx, f.spec.Key, "import-create", 1<<20)
	must(t, err)
	a := crosshost.Artifact{Version: crosshost.Version, Source: crosshost.Source{HostID: strings.Repeat("e", 64), StateID: strings.Repeat("d", 32), WorkspaceID: f.ready.Workspace.ID, Key: f.spec.Key, TransferID: "export", ApprovalDigest: strings.Repeat("c", 64)}, Destination: o.Destination, CheckpointRepository: f.ready.Workspace.CheckpointRepository, Seal: f.ready.Workspace.Checkpoint.Seal, ManifestDigest: f.ready.Workspace.Checkpoint.Digest}
	for n, r := range f.ready.Workspace.Members {
		a.Members = append(a.Members, crosshost.Member{Name: r.Name, Repository: r.Repository, Branch: r.Branch, IntegrationBase: r.Base, Commit: f.ready.Workspace.Checkpoint.Members[n].Commit})
	}
	raw, err := crosshost.Seal(a)
	must(t, err)
	a, err = crosshost.Parse(raw)
	must(t, err)
	return f, m, a
}
func TestReviewedImportUsesSameCardOriginalRefsFreshIdentityAndNoReplayGrant(t *testing.T) {
	f, m, a := incomingFixture(t)
	ctx := context.Background()
	out, err := m.Import(ctx, a)
	must(t, err)
	if out == nil || out.Phase != "completed" || out.GrantToken != "" {
		t.Fatalf("missing completed inventory: %+v", out)
	}
	b := snapshot(t, f.dest, f.spec.Key)
	if b.Lease != nil || b.Workspace.Phase != "ready" || b.Workspace.ID != a.Destination.WorkspaceID || b.Workspace.ID == f.ready.Workspace.ID || b.Workspace.Checkpoint == nil || b.Workspace.Checkpoint.Seal == a.Seal || b.Workspace.Checkpoint.Previous != "" {
		t.Fatal("not a fresh unleased genesis")
	}
	for n, r := range b.Workspace.Members {
		if r.Branch != a.Members[n].Branch || r.Base != "main" || git(t, r.Path, "rev-parse", "HEAD") != a.Members[n].Commit {
			t.Fatal("original task ref/base/vector lost")
		}
	}
	held, err := f.dest.AcquireWorkspaceContext(ctx, f.spec.Key, "initialization")
	must(t, err)
	replay, err := m.ContinueImport(ctx, f.spec.Key, a.Destination.CreationID)
	must(t, err)
	raw, _ := json.Marshal(replay)
	if replay.GrantToken != "" || replay.RootPath != "" || strings.Contains(string(raw), held.Lease.Token) {
		t.Fatal("historical import exposed later writer")
	}
	if _, err = f.dest.StageWorkspaceContext(ctx, a.Destination.CreationID, workspace.Spec{Key: f.spec.Key, Repositories: f.spec.Repositories, CheckpointRepository: "a", QuotaBytes: 1 << 20}); err == nil {
		t.Fatal("ordinary creation resumed incoming transfer")
	}
}
