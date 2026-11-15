package workspacetransfer_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacetransfer"
)

func missingFixture(t *testing.T) (*fixture, *workspacetransfer.MissingPlan) {
	t.Helper()
	f := newFixture(t)
	// Only this test's disposable checkout is removed. The administrative index
	// and exact sealed remote objects deliberately remain registered.
	must(t, os.RemoveAll(f.source.Workspace.Members[1].Path))
	p, err := f.m.InspectMissing(context.Background(), "source", "target", "missing-transfer", 2<<20)
	must(t, err)
	if p.Members[0].Present == nil || p.Members[1].Missing == nil || p.Checkpoint.Seal != f.source.Workspace.Checkpoint.Seal {
		t.Fatal("missing observation lost the exact mixed source vector")
	}
	raw, err := p.Bytes()
	must(t, err)
	parsed, err := workspacetransfer.ParseMissingPlan(raw)
	must(t, err)
	return f, &parsed
}

func TestMissingApprovalRechecksIndexBeforeRetiringSource(t *testing.T) {
	f, p := missingFixture(t)
	before := f.stateBytes()
	index := filepath.Join(p.Members[1].Missing.Admin, "index")
	raw, err := os.ReadFile(index)
	must(t, err)
	must(t, os.WriteFile(index, append(raw, 'x'), 0600))
	if _, err = f.m.BeginMissing(context.Background(), *p); err == nil {
		t.Fatal("stale missing index approval retired the source")
	}
	if !bytes.Equal(before, f.stateBytes()) || f.row("source").Workspace.Transfer != nil || f.row("target") != nil {
		t.Fatal("failed reinspection changed durable source or target authority")
	}
}

func TestMissingWholePreflightRefusesLaterMemberBeforeFirstDetach(t *testing.T) {
	f, p := missingFixture(t)
	_, err := f.m.BeginMissing(context.Background(), *p)
	must(t, err)
	index := filepath.Join(p.Members[1].Missing.Admin, "index")
	original, err := os.ReadFile(index)
	must(t, err)
	must(t, os.WriteFile(index, append(original, 'x'), 0600))
	out, err := f.m.ContinueMissing(context.Background(), "source", p.ID)
	if err == nil || out == nil || out.GrantToken != "" || f.row("target") != nil {
		t.Fatal("later stale member admitted target creation or capability", err)
	}
	first := f.source.Workspace.Members[0]
	if got := git(t, first.Path, "symbolic-ref", "HEAD"); got != "refs/heads/"+first.Branch {
		t.Fatal("whole-vector preflight detached an earlier member", got)
	}
	if f.row("source").Workspace.Transfer.Phase != "relinquishing" {
		t.Fatal("stale continuation rolled back permanent source exclusion")
	}
	must(t, os.WriteFile(index, original, 0600))
	out, err = f.m.ContinueMissing(context.Background(), "source", p.ID)
	must(t, err)
	if out.Phase != "completed" || out.GrantToken != "" || f.row("target").Workspace.Phase != "ready" {
		t.Fatal("exact restored observation did not finish recovery")
	}
}

func TestMissingWholeWorkspaceRootRecoversOnlyConfirmedSeal(t *testing.T) {
	f := newFixture(t)
	must(t, os.RemoveAll(f.source.Path))
	p, err := f.m.InspectMissing(context.Background(), "source", "target", "root-missing", 2<<20)
	must(t, err)
	for _, member := range p.Members {
		if member.Missing == nil || member.Missing.MissingAt != f.source.Path {
			t.Fatal("whole-root absence was represented as independent missing descendants")
		}
	}
	_, err = f.m.BeginMissing(context.Background(), *p)
	must(t, err)
	out, err := f.m.ContinueMissing(context.Background(), "source", p.ID)
	must(t, err)
	if out.Phase != "completed" || out.TargetID == p.SourceID || f.row("source").Lease != nil {
		t.Fatal("missing-root recovery reused source ownership")
	}
	if _, err = os.Lstat(f.source.Path); !os.IsNotExist(err) {
		t.Fatal("missing-root recovery recreated the source checkout", err)
	}
	target := f.row("target")
	for i, member := range target.Workspace.Members {
		if git(t, member.Path, "rev-parse", "HEAD") != p.Checkpoint.Members[i].Commit || member.Branch != f.source.Workspace.Members[i].Branch {
			t.Fatal("missing-root recovery substituted a newer tip or distinct task ref")
		}
	}
}
