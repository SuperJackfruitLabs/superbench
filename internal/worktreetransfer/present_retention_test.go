package worktreetransfer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func presentFixture(t *testing.T) (*fixture, MissingManager, PresentRetention, string) {
	t.Helper()
	f := newFixture(t)
	f.git(t, f.checkout, "checkout", "--detach", f.commit)
	write(t, filepath.Join(f.checkout, "tracked"), "index-only staged evidence\n")
	f.git(t, f.checkout, "add", "tracked")
	blob := f.git(t, f.checkout, "rev-parse", ":tracked")
	write(t, filepath.Join(f.checkout, "tracked"), "original\n")
	m := MissingManager{Git: f.m.Git, Gate: func(ctx context.Context, _ MissingPlan) error { return ctx.Err() }}
	p, e := m.InspectPresentRetention(context.Background(), MissingSpec{ID: "archive-proof", Checkout: f.checkout, Common: f.repo, TaskRef: "refs/heads/task", Commit: f.commit})
	if e != nil {
		t.Fatal(e)
	}
	return f, m, p, blob
}
func TestPresentRetentionPinsCopiedIndexWithoutMissingCheckoutAuthority(t *testing.T) {
	f, m, p, blob := presentFixture(t)
	ctx := context.Background()
	if e := m.RetainPresentAdministration(ctx, p); e != nil {
		t.Fatal(e)
	}
	if e := m.VerifyPresentRetention(ctx, p, true); e != nil {
		t.Fatal(e)
	}
	if e := m.RetainPresentAdministration(ctx, p); e != nil {
		t.Fatal("exact retention retry", e)
	}
	if e := m.Apply(ctx, p.Plan); e == nil {
		t.Fatal("present evidence accepted as absent checkout mutation authority")
	}
	head := f.git(t, f.checkout, "rev-parse", "HEAD")
	if head != f.commit {
		t.Fatal("present HEAD changed")
	}
	if raw, e := os.ReadFile(filepath.Join(f.checkout, "tracked")); e != nil || string(raw) != "original\n" {
		t.Fatal("checkout bytes changed", e)
	}
	if e := os.RemoveAll(f.checkout); e != nil {
		t.Fatal(e)
	}
	f.git(t, f.repo, "gc", "--prune=now")
	if f.git(t, f.repo, "cat-file", "blob", blob) != "index-only staged evidence" {
		t.Fatal("staged evidence pruned")
	}
	if f.git(t, f.repo, "--git-dir="+p.Plan.Admin, "rev-parse", ":tracked") != blob {
		t.Fatal("owned original index lost")
	}
}
func TestPresentRetentionFailurePreservesOwnerAndRejectsChangedEvidence(t *testing.T) {
	f, m, p, _ := presentFixture(t)
	index := filepath.Join(p.Plan.Admin, "index")
	raw, e := os.ReadFile(index)
	if e != nil {
		t.Fatal(e)
	}
	calls := 0
	m.Gate = func(context.Context, MissingPlan) error {
		calls++
		if calls == 2 {
			return os.WriteFile(index, append(append([]byte{}, raw...), byte('x')), 0600)
		}
		return nil
	}
	if e = m.RetainPresentAdministration(context.Background(), p); e == nil {
		t.Fatal("late index mutation accepted")
	}
	if _, e = os.Lstat(p.Plan.Evidence); !os.IsNotExist(e) {
		t.Fatal("stale admin evidence published", e)
	}
	if _, e = os.Lstat(filepath.Join(p.Plan.Admin, "locked")); !os.IsNotExist(e) {
		t.Fatal("stale mutation locked source", e)
	}
	if e = os.WriteFile(index, raw, 0600); e != nil {
		t.Fatal(e)
	}
	m.Gate = func(context.Context, MissingPlan) error { return nil }
	if e = m.RetainPresentAdministration(context.Background(), p); e != nil {
		t.Fatal(e)
	}
	ref := m.refPrefix(p.Plan) + p.Plan.Objects[0].OID
	f.git(t, f.repo, "update-ref", "-d", ref)
	if e = m.VerifyPresentRetention(context.Background(), p, true); e == nil {
		t.Fatal("missing local retention root accepted")
	}
	bad := p
	bad.Version = "superbench.workspace-missing-transfer.v1"
	if e = m.VerifyPresentRetention(context.Background(), bad, true); e == nil {
		t.Fatal("wrong mode accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e = m.VerifyPresentRetention(ctx, p, true); !errors.Is(e, context.Canceled) {
		t.Fatal("cancelled verification", e)
	}
}
