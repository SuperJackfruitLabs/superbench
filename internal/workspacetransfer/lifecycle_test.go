package workspacetransfer_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReviewedTransferPreservesSourceBytesAndHandsOriginalRefsToFreshReadyWorkspace(t *testing.T) {
	f := newFixture(t)
	p := f.plan()
	index := map[string][]byte{}
	gitfiles := map[string][]byte{}
	for _, m := range f.source.Workspace.Members {
		admin := git(t, m.Path, "rev-parse", "--absolute-git-dir")
		index[m.Name] = read(t, filepath.Join(admin, "index"))
		gitfiles[m.Name] = read(t, filepath.Join(m.Path, ".git"))
	}
	before := f.stateBytes()
	r, err := f.m.Begin(context.Background(), *p)
	must(t, err)
	if r.Phase != "started" || r.GrantToken != "" || r.RootPath != "" {
		t.Fatal("begin exported destination authority")
	}
	blocked := f.row("source")
	if blocked.Workspace.Transfer == nil || blocked.Workspace.Transfer.Phase != "relinquishing" || blocked.Lease != nil {
		t.Fatal("source not durably blocked")
	}
	if bytes.Equal(before, f.stateBytes()) {
		t.Fatal("begin omitted durable approval")
	}
	for _, m := range blocked.Workspace.Members {
		if got := git(t, m.Path, "symbolic-ref", "HEAD"); got != "refs/heads/"+m.Branch {
			t.Fatal("begin detached before approval")
		}
	}
	if b, err := f.store.AcquireWorkspaceOwnerContext(context.Background(), "source", "writer"); err == nil || b != nil && b.Lease != nil {
		t.Fatal("source acquired after relinquishing")
	}
	if b, err := f.store.StageWorkspaceContext(context.Background(), "source-create", f.spec); err == nil || b != nil && b.Lease != nil {
		t.Fatal("old creation obtained source grant")
	}
	replay, err := f.m.Begin(context.Background(), *p)
	must(t, err)
	if replay.Phase != "started" || replay.GrantToken != "" {
		t.Fatal("lost begin reply changed authority")
	}
	r, err = f.m.Continue(context.Background(), "source", p.ID)
	must(t, err)
	if r.Phase != "completed" || r.GrantToken != "" || r.RootPath == "" {
		t.Fatal("transfer did not complete warm whole-vector handoff")
	}
	source, target := f.row("source"), f.row("target")
	if source.Workspace.Transfer.Phase != "transferred" || target.Workspace.ID != p.TargetID || target.Workspace.ID == source.Workspace.ID || target.Workspace.TransferredFrom.SourceID != source.Workspace.ID || target.Workspace.Phase != "ready" || target.Lease != nil || target.RequestedQuotaBytes != 2<<20 {
		t.Fatal("incorrect destination identity, budget or readiness")
	}
	provenance := target.Workspace.RecoveredFrom
	if provenance == nil || provenance.Repository != source.Workspace.Members[0].Repository || provenance.BenchID != source.Workspace.ID || provenance.Commit != source.Workspace.Checkpoint.Seal || provenance.ManifestSHA256 != source.Workspace.Checkpoint.Digest {
		t.Fatal("transfer lost exact source checkpoint provenance")
	}
	if f.journal(p.ID).Phase != "completed" {
		t.Fatal("completion not journaled")
	}
	for i, m := range source.Workspace.Members {
		admin := git(t, m.Path, "rev-parse", "--absolute-git-dir")
		if !bytes.Equal(index[m.Name], read(t, filepath.Join(admin, "index"))) || !bytes.Equal(gitfiles[m.Name], read(t, filepath.Join(m.Path, ".git"))) {
			t.Fatal("source index or registration rewritten")
		}
		if got := string(read(t, filepath.Join(m.Path, "private-output"))); got != "source-only:"+m.Name {
			t.Fatal("source private bytes changed")
		}
		if got := string(read(t, filepath.Join(admin, "HEAD"))); got != source.Workspace.Checkpoint.Members[i].Commit+"\n" {
			t.Fatal("source not detached at sealed commit")
		}
		tm := target.Workspace.Members[i]
		if tm.Branch != m.Branch || tm.Base != m.Base || tm.Base != "main" || git(t, tm.Path, "symbolic-ref", "HEAD") != "refs/heads/"+m.Branch {
			t.Fatal("target did not own original ref")
		}
		if _, err := os.Lstat(filepath.Join(tm.Path, "private-output")); !os.IsNotExist(err) {
			t.Fatal("source private bytes copied to target")
		}
	}
	ready := f.stateBytes()
	r, err = f.m.Continue(context.Background(), "source", p.ID)
	must(t, err)
	if r.GrantToken != "" || !bytes.Equal(ready, f.stateBytes()) {
		t.Fatal("completed retry changed evidence or granted")
	}
	owner, err := f.store.AcquireWorkspaceOwnerContext(context.Background(), "target", "next-owner")
	must(t, err)
	held := f.stateBytes()
	for _, call := range []func() error{func() error {
		out, e := f.m.Continue(context.Background(), "source", p.ID)
		if out != nil && (out.GrantToken != "" || out.RootPath != "") {
			t.Fatal("historical continue leaked later owner")
		}
		return e
	}, func() error {
		out, e := f.m.Begin(context.Background(), *p)
		if out != nil && (out.GrantToken != "" || out.RootPath != "") {
			t.Fatal("historical begin leaked later owner")
		}
		return e
	}} {
		_ = call()
	}
	if owner.Lease.Token == "" || !bytes.Equal(held, f.stateBytes()) {
		t.Fatal("replay changed later ownership")
	}
	if _, err = f.store.AcquireWorkspaceOwnerContext(context.Background(), "source", "writer"); err == nil {
		t.Fatal("transferred source regained ownership")
	}
}
