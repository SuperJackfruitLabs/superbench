package mirrorstage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func managerFixture(t *testing.T) (Manager, *Intent) {
	t.Helper()
	state, parent, mirror := fixture(t)
	control := filepath.Join(filepath.Dir(state), "controlled")
	p, e := CreateManaged(state, parent, mirror, control)
	if e != nil {
		t.Fatal(e)
	}
	var mu sync.Mutex
	held := false
	m := Manager{State: state, Parent: parent, Within: func(fn func() error) error {
		mu.Lock()
		defer mu.Unlock()
		held = true
		defer func() { held = false }()
		return fn()
	}, VerifyControl: func(ctx context.Context, state, root string) error {
		if !held || state != p.State || root != control {
			t.Fatal("managed reconciliation outside host lock or changed provenance")
		}
		return ctx.Err()
	}}
	return m, p
}
func put(t *testing.T, path, body string) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
}
func exists(path string) bool { _, e := os.Lstat(path); return e == nil }
func inspect(t *testing.T, m Manager, p *Intent) Plan {
	t.Helper()
	plan, e := m.Inspect(context.Background(), p.ID)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := plan.Bytes()
	if e != nil {
		t.Fatal(e)
	}
	parsed, e := ParsePlan(raw)
	if e != nil || !samePlan(*plan, *parsed) {
		t.Fatal("plan roundtrip", e)
	}
	return *plan
}

func TestReviewedCollectionPreservesExternalFilesAndForgetsOnlyOwnedMetadata(t *testing.T) {
	m, p := managerFixture(t)
	put(t, filepath.Join(p.Stage, "repo.git", "objects", "candidate"), "partial clone")
	outside := filepath.Join(filepath.Dir(p.Parent), "outside")
	put(t, outside, "unrelated bytes")
	if e := os.Symlink(outside, filepath.Join(p.Stage, "outside-link")); e != nil {
		t.Fatal(e)
	}
	plan := inspect(t, m, p)
	result, e := m.Apply(context.Background(), plan)
	if e != nil || result == nil || !result.Completed {
		t.Fatal(result, e)
	}
	if exists(p.Stage) || exists(p.Mirror) {
		t.Fatal("collection left stage or published a mirror")
	}
	if raw, e := os.ReadFile(outside); e != nil || string(raw) != "unrelated bytes" {
		t.Fatal("followed external symlink", e)
	}
	retained, e := m.Inspect(context.Background(), p.ID)
	if e != nil || !samePlan(plan, *retained) {
		t.Fatal("lost historical approval", e)
	}
	if _, e = m.Apply(context.Background(), plan); e != nil {
		t.Fatal("terminal replay", e)
	}
	if e = os.Mkdir(p.Stage, 0700); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Apply(context.Background(), plan); e == nil {
		t.Fatal("terminal replay adopted returned empty root")
	}
	if e = m.ForgetCompleted(context.Background(), p.ID, plan.Digest); e == nil {
		t.Fatal("forget adopted returned root")
	}
	if !exists(p.Stage) {
		t.Fatal("removed returned root")
	}
	if e = os.Remove(p.Stage); e != nil {
		t.Fatal(e)
	} // Explicit fixture restoration only.
	dir := filepath.Join(p.State, "mirror-stage-collections")
	unknown := filepath.Join(dir, "foreign.json")
	put(t, unknown, "foreign authority")
	receipt := filepath.Join(p.State, "receipts", "request")
	put(t, receipt, "exact historical receipt")
	if e = m.ForgetCompleted(context.Background(), p.ID, plan.Digest); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Apply(context.Background(), plan); e == nil {
		t.Fatal("forgotten approval regained deletion authority")
	}
	entries, e := os.ReadDir(dir)
	if e != nil || len(entries) != 1 || entries[0].Name() != "foreign.json" {
		t.Fatal("incorrect metadata retirement", entries, e)
	}
	for path, want := range map[string]string{unknown: "foreign authority", receipt: "exact historical receipt"} {
		if raw, e := os.ReadFile(path); e != nil || string(raw) != want {
			t.Fatal("changed unrelated evidence", path, e)
		}
	}
}
func TestChangesBeforeApprovalRefuseWithoutDeletion(t *testing.T) {
	for _, change := range []string{"changed", "new", "missing"} {
		t.Run(change, func(t *testing.T) {
			m, p := managerFixture(t)
			a := filepath.Join(p.Stage, "a")
			put(t, a, "original")
			plan := inspect(t, m, p)
			switch change {
			case "changed":
				put(t, a, "changed")
			case "new":
				put(t, filepath.Join(p.Stage, "unapproved"), "new work")
			case "missing":
				if e := os.Remove(a); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := m.Apply(context.Background(), plan); e == nil {
				t.Fatal("accepted changed initial tree")
			}
			if !exists(p.Stage) || exists(filepath.Join(p.State, "mirror-stage-collections")) {
				t.Fatal("changed tree admitted before approval")
			}
		})
	}
}
func TestInterruptedDeletionAllowsOnlyMissingApprovedEntries(t *testing.T) {
	m, p := managerFixture(t)
	put(t, filepath.Join(p.Stage, "a"), "keep until retry")
	put(t, filepath.Join(p.Stage, "z"), "first deletion")
	plan := inspect(t, m, p)
	verify := m.VerifyControl
	m.VerifyControl = func(ctx context.Context, state, root string) error {
		if e := verify(ctx, state, root); e != nil {
			return e
		}
		if !exists(filepath.Join(p.Stage, "z")) && exists(filepath.Join(p.Stage, "a")) {
			return errors.New("injected reconciliation failure")
		}
		return nil
	}
	if _, e := m.Apply(context.Background(), plan); e == nil {
		t.Fatal("did not exercise interruption")
	}
	if exists(filepath.Join(p.Stage, "z")) || !exists(filepath.Join(p.Stage, "a")) {
		t.Fatal("unexpected partial progress")
	}
	m.VerifyControl = verify
	newFile := filepath.Join(p.Stage, "unapproved")
	put(t, newFile, "must survive")
	if _, e := m.Apply(context.Background(), plan); e == nil {
		t.Fatal("adopted new entry during retry")
	}
	if !exists(filepath.Join(p.Stage, "a")) || !exists(newFile) {
		t.Fatal("deleted after failed whole preflight")
	}
	if e := os.Remove(newFile); e != nil {
		t.Fatal(e)
	}
	if _, e := m.Apply(context.Background(), plan); e != nil {
		t.Fatal(e)
	}
}
func TestRootRemovalIntentRequiresFreshEmptyRootReview(t *testing.T) {
	m, p := managerFixture(t)
	put(t, filepath.Join(p.Stage, "a"), "approved")
	plan := inspect(t, m, p)
	verify := m.VerifyControl
	removalPath := filepath.Join(p.State, "mirror-stage-collections", plan.removalName())
	m.VerifyControl = func(ctx context.Context, state, root string) error {
		if e := verify(ctx, state, root); e != nil {
			return e
		}
		if exists(removalPath) && exists(p.Stage) {
			return errors.New("fault after removal intent before unlink")
		}
		return nil
	}
	if _, e := m.Apply(context.Background(), plan); e == nil {
		t.Fatal("did not interrupt root removal")
	}
	if !exists(removalPath) || !exists(p.Stage) {
		t.Fatal("missing durable removal boundary")
	}
	m.VerifyControl = verify
	if _, e := m.Apply(context.Background(), plan); e == nil {
		t.Fatal("same inode/mode authorized ambiguous root removal")
	}
	review, e := m.InspectRootRemoval(context.Background(), p.ID)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := review.Bytes()
	if e != nil {
		t.Fatal(e)
	}
	parsed, e := ParseRootRemovalReview(raw)
	if e != nil || *parsed != *review {
		t.Fatal("root review roundtrip", e)
	}
	sentinel := filepath.Join(p.Stage, "new-work")
	put(t, sentinel, "preserve")
	if _, e = m.ReconcileRootRemoval(context.Background(), *review); e == nil {
		t.Fatal("removed nonempty root after review")
	}
	if !exists(sentinel) {
		t.Fatal("deleted unapproved sentinel")
	}
	if e = os.Remove(sentinel); e != nil {
		t.Fatal(e)
	}
	reviewPath := filepath.Join(p.State, "mirror-stage-collections", p.ID+"-review-"+review.Digest+".json")
	m.VerifyControl = func(ctx context.Context, state, root string) error {
		if e := verify(ctx, state, root); e != nil {
			return e
		}
		if exists(reviewPath) && exists(p.Stage) {
			return errors.New("fault after single-use fresh review")
		}
		return nil
	}
	if _, e = m.ReconcileRootRemoval(context.Background(), *review); e == nil {
		t.Fatal("did not interrupt fresh review")
	}
	m.VerifyControl = verify
	if _, e = m.ReconcileRootRemoval(context.Background(), *review); e == nil {
		t.Fatal("reused present-root approval")
	}
	fresh, e := m.InspectRootRemoval(context.Background(), p.ID)
	if e != nil {
		t.Fatal(e)
	}
	if fresh.Digest == review.Digest {
		t.Fatal("fresh observation reused approval")
	}
	if _, e = m.ReconcileRootRemoval(context.Background(), *fresh); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Apply(context.Background(), plan); e != nil {
		t.Fatal("terminal original approval replay", e)
	}
	if e = m.ForgetCompleted(context.Background(), p.ID, plan.Digest); e != nil {
		t.Fatal(e)
	}
}
func TestRootUnlinkAndOwnershipRetirementFailuresResumeFromExactEvidence(t *testing.T) {
	for _, boundary := range []string{"after-unlink", "after-root-record"} {
		t.Run(boundary, func(t *testing.T) {
			m, p := managerFixture(t)
			put(t, filepath.Join(p.Stage, "a"), "approved")
			plan := inspect(t, m, p)
			verify := m.VerifyControl
			done := filepath.Join(p.State, "mirror-stage-collections", plan.doneName())
			rootRecord := filepath.Join(p.State, "mirror-stages", p.ID+"-root.json")
			m.VerifyControl = func(ctx context.Context, state, root string) error {
				if e := verify(ctx, state, root); e != nil {
					return e
				}
				if boundary == "after-unlink" && !exists(p.Stage) && !exists(done) || boundary == "after-root-record" && exists(done) && !exists(rootRecord) {
					return errors.New("injected finalization failure")
				}
				return nil
			}
			if _, e := m.Apply(context.Background(), plan); e == nil {
				t.Fatal("did not reach failure boundary")
			}
			if exists(p.Stage) {
				t.Fatal("root unlink boundary not reached")
			}
			m.VerifyControl = verify
			if _, e := m.Apply(context.Background(), plan); e != nil {
				t.Fatal(e)
			}
			if e := m.ForgetCompleted(context.Background(), p.ID, plan.Digest); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestManagedProofAndMissingOwnershipAreMandatory(t *testing.T) {
	m, p := managerFixture(t)
	put(t, filepath.Join(p.Stage, "a"), "do not delete")
	cooperative, e := Create(p.State, p.Parent, p.Mirror)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Inspect(context.Background(), cooperative.ID); e == nil {
		t.Fatal("cooperative provenance became fencing proof")
	}
	if e = m.CleanupPublished(context.Background(), cooperative.ID); e == nil {
		t.Fatal("cooperative empty cleanup inferred fence")
	}
	rootRecord := filepath.Join(p.State, "mirror-stages", p.ID+"-root.json")
	if e = os.Remove(rootRecord); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Inspect(context.Background(), p.ID); e == nil {
		t.Fatal("adopted stage without root ownership")
	}
	if !exists(filepath.Join(p.Stage, "a")) {
		t.Fatal("changed protected stage")
	}
	m.Within = nil
	if _, e = m.Inspect(context.Background(), p.ID); e == nil {
		t.Fatal("missing host lock accepted")
	}
}
func TestPublishedCleanupRetiresOnlyItsEmptyStageWithinOneLock(t *testing.T) {
	m, p := managerFixture(t)
	put(t, filepath.Join(p.Mirror, "HEAD"), "published mirror")
	if e := m.CleanupPublished(context.Background(), p.ID); e != nil {
		t.Fatal(e)
	}
	if exists(p.Stage) {
		t.Fatal("empty owned stage retained")
	}
	raw, e := os.ReadFile(filepath.Join(p.Mirror, "HEAD"))
	if e != nil || string(raw) != "published mirror" {
		t.Fatal("changed published mirror", e)
	}
	for _, name := range []string{"mirror-stages", "mirror-stage-collections"} {
		entries, e := os.ReadDir(filepath.Join(p.State, name))
		if e != nil || len(entries) != 0 {
			t.Fatal("normal cycle leaked owned metadata", name, entries, e)
		}
	}
	// A new stage must never collect leftovers implicitly after publication.
	next, e := CreateManaged(p.State, p.Parent, p.Mirror, p.ControlRoot)
	if e != nil {
		t.Fatal(e)
	}
	put(t, filepath.Join(next.Stage, "unapproved"), "preserve")
	if e = m.CleanupPublished(context.Background(), next.ID); e == nil {
		t.Fatal("automatically deleted nonempty published stage")
	}
	if !exists(filepath.Join(next.Stage, "unapproved")) {
		t.Fatal("deleted unapproved file")
	}
}
func TestForgetInterruptedAfterDisablingPlanNeverReauthorizesOldApply(t *testing.T) {
	m, p := managerFixture(t)
	plan := inspect(t, m, p)
	if _, e := m.Apply(context.Background(), plan); e != nil {
		t.Fatal(e)
	}
	planPath := filepath.Join(p.State, "mirror-stage-collections", plan.planName())
	marker := filepath.Join(p.State, "mirror-stage-collections", p.ID+"-forget.json")
	verify := m.VerifyControl
	m.VerifyControl = func(ctx context.Context, state, root string) error {
		if e := verify(ctx, state, root); e != nil {
			return e
		}
		if exists(marker) && !exists(planPath) {
			return errors.New("fault after disabling original approval")
		}
		return nil
	}
	if e := m.ForgetCompleted(context.Background(), p.ID, plan.Digest); e == nil {
		t.Fatal("did not interrupt metadata retirement")
	}
	if exists(planPath) || !exists(marker) {
		t.Fatal("did not reach approved metadata boundary")
	}
	m.VerifyControl = verify
	if _, e := m.Apply(context.Background(), plan); e == nil {
		t.Fatal("old plan regained authority")
	}
	if e := os.Mkdir(p.Stage, 0700); e != nil {
		t.Fatal(e)
	}
	if e := m.ForgetCompleted(context.Background(), p.ID, plan.Digest); e == nil {
		t.Fatal("forget deleted metadata across returned root")
	}
	if !exists(marker) || !exists(p.Stage) {
		t.Fatal("forgotten evidence or returned root lost")
	}
	if e := os.Remove(p.Stage); e != nil {
		t.Fatal(e)
	}
	if e := m.ForgetCompleted(context.Background(), p.ID, plan.Digest); e != nil {
		t.Fatal(e)
	}
}
func TestPlanParserRejectsAmbiguousAndOversizedInput(t *testing.T) {
	m, p := managerFixture(t)
	plan := inspect(t, m, p)
	raw, e := plan.Bytes()
	if e != nil {
		t.Fatal(e)
	}
	for _, bad := range [][]byte{[]byte(`{"Version":"foreign",` + string(raw[1:])), []byte(`{"Unknown":true,` + string(raw[1:])), append(append([]byte{}, raw...), []byte(`{}`)...), []byte(strings.Repeat(" ", MaxPlan+1))} {
		if _, e = ParsePlan(bad); e == nil {
			t.Fatal("accepted ambiguous input")
		}
	}
}

func TestCallerCannotSelectForeignFencingProvenance(t *testing.T) {
	m, p := managerFixture(t)
	put(t, filepath.Join(p.Stage, "a"), "protected")
	plan := inspect(t, m, p)
	plan.Intent.ControlRoot = filepath.Join(filepath.Dir(p.Parent), "foreign-control")
	plan.Digest = plan.digest()
	called := false
	m.VerifyControl = func(context.Context, string, string) error { called = true; return nil }
	if _, e := m.Apply(context.Background(), plan); e == nil {
		t.Fatal("accepted caller-selected control authority")
	}
	if called {
		t.Fatal("unverified caller provenance reached fencing callback")
	}
	if !exists(filepath.Join(p.Stage, "a")) {
		t.Fatal("changed protected bytes")
	}
}

func TestCollectionBudgetLeavesRoomToRetireCompletedMetadata(t *testing.T) {
	m, p := managerFixture(t)
	plan := inspect(t, m, p)
	if _, e := m.Apply(context.Background(), plan); e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(p.State, "mirror-stage-collections")
	// Three completed records plus foreign entries consume1999 slots; the final
	// slot must remain sufficient for an explicit durable forget marker.
	for i := 0; i < MaxJournalEntries-4; i++ {
		put(t, filepath.Join(dir, fmt.Sprintf("foreign-%04d", i)), "unknown")
	}
	if e := m.ForgetCompleted(context.Background(), p.ID, plan.Digest); e != nil {
		t.Fatal("completed metadata cannot be retired at reserved capacity", e)
	}
	entries, e := os.ReadDir(dir)
	if e != nil || len(entries) != MaxJournalEntries-4 {
		t.Fatal("changed unknown entries", len(entries), e)
	}
	next, e := CreateManaged(p.State, p.Parent, p.Mirror, p.ControlRoot)
	if e != nil {
		t.Fatal(e)
	}
	nextPlan := inspect(t, m, next)
	put(t, filepath.Join(dir, "foreign-last"), "unknown")
	if _, e = m.Apply(context.Background(), nextPlan); e == nil {
		t.Fatal("admitted collection without completion+forget capacity")
	}
	if !exists(next.Stage) {
		t.Fatal("budget refusal removed root")
	}
	if exists(filepath.Join(dir, nextPlan.planName())) {
		t.Fatal("budget refusal created approval")
	}
}
