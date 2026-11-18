//go:build linux && freshstoragefixture

package fixture

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/allocationledger"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshrecovery"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacebootstrap"
)

// stalledReclaims proves on real XFS the root side of a create whose
// bootstrap completed and whose initialization stalled (the daemon's record
// stays initializing): root's reclaim decides from its own checks alone. A
// process left in the bench refuses it with no effect; once the bench is
// idle the reclaim purges, removes and retires as for any reclaim, and the
// key makes its next generation, which is reclaimed in turn.
func (e *env) stalledReclaims() {
	t := e.t
	key := "stalled-init"
	b, err := e.bootstrap(key)
	if err != nil {
		t.Fatal(err)
	}
	if p := e.intentPhase(key); p != "completed" {
		t.Fatalf("stalled bench intent %s", p)
	}
	stop := e.inUse(b)
	if _, err = e.reclaim(t.Context(), b); !errors.Is(err, freshrecovery.ErrReclaimBusy) || strings.Contains(err.Error(), "effects may have occurred") {
		stop()
		t.Fatalf("a stalled bench still in use: %v", err)
	}
	stop()
	if p := e.intentPhase(key); p != "completed" || e.row(key) != b.allocation {
		t.Fatalf("a refused reclaim had effects: %s %+v", p, e.row(key))
	}
	t.Log("LOWER_STALLED_CREATE_IN_USE_REFUSED_PASS")
	got, err := e.reclaim(t.Context(), b)
	if err != nil {
		t.Fatal("stalled bench reclaim:", err)
	}
	e.assertReclaimed(b, got)
	t.Log("LOWER_STALLED_CREATE_RECLAIMED_PASS")

	policy, err := payloadexec.LoadFixedPolicy()
	if err != nil {
		t.Fatal(err)
	}
	second := successor(b, "stalled-second")
	record := &model.Bench{Key: key, Workspace: &model.Workspace{ID: second.ID, Generation: second.Generation, Previous: second.Previous}}
	if err = workspacebootstrap.MoveLayoutAside(policy.StateRoot, record); err != nil {
		t.Fatal(err)
	}
	next, err := e.next(second, "stalled-second")
	if err != nil || next.allocation.ProjectID <= b.allocation.ProjectID || e.row(key) != next.allocation {
		t.Fatalf("next generation after a stalled bench: %+v %v", next.allocation, err)
	}
	t.Log("LOWER_STALLED_CREATE_NEXT_GENERATION_PASS")
	got, err = e.reclaim(t.Context(), next)
	if err != nil {
		t.Fatal(err)
	}
	e.assertReclaimed(next, got)
	t.Log("LOWER_STALLED_CREATE_NEXT_GENERATION_RECLAIMED_PASS")
}

// secondFailure proves that a key's second failed create is recovered as the
// first was: each generation's cleanup and retirement approvals have their
// own names (KEY.* for the first, KEY@2.* for the second), so the second
// recovery neither collides with nor touches the first's, and a third
// generation then starts in place of the second's tombstone.
func (e *env) secondFailure() {
	t := e.t
	ctx := t.Context()
	key := "failed-twice"
	s := e.template.Spec
	s.Key, s.ID, s.QuotaBytes = key, hash("failed-bid:" + key)[:32], 0
	first := e.failCreate(s, key)
	e.recoverFailed(first)
	cleanups, retirements := filepath.Join(e.p.Ledger, "payload-cleanups"), filepath.Join(e.p.Ledger, "payload-retirements")
	firstFiles := map[string][]byte{}
	for _, path := range []string{filepath.Join(cleanups, key+".json"), filepath.Join(cleanups, key+".leaf.json"), filepath.Join(retirements, key+".json")} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal("the first recovery's approval:", err)
		}
		firstFiles[path] = raw
	}

	// The second generation fails too, after it took a new project.
	second := e.failCreate(successor(first.bench, "failed-twice-second"), key+"-second")
	if second.allocation.ProjectID <= first.allocation.ProjectID {
		t.Fatalf("second generation project %d after %d", second.allocation.ProjectID, first.allocation.ProjectID)
	}
	if _, err := freshrecovery.InspectRetired(ctx, second.spec); err == nil {
		t.Fatal("root proved the second failed create retired before its recovery")
	}
	e.recoverFailed(second)
	stem := payloadbootstrap.Stem(second.spec)
	for _, path := range []string{filepath.Join(cleanups, stem+".json"), filepath.Join(cleanups, stem+".leaf.json"), filepath.Join(retirements, stem+".json")} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatal("the second recovery's own approval:", err)
		}
	}
	for path, raw := range firstFiles {
		assertBytes(t, path, raw)
	}
	got, err := freshrecovery.InspectRetired(ctx, second.spec)
	if err != nil || got.Generation != 2 || got.ID != second.spec.ID || got.Allocation.ProjectID != second.allocation.ProjectID {
		t.Fatalf("root's inspection of the second failed create: %+v %v", got, err)
	}
	t.Logf("first failed project %d, second failed project %d (%s)", first.allocation.ProjectID, second.allocation.ProjectID, stem)
	t.Log("LOWER_SECOND_FAILED_GENERATION_RECOVERED_PASS")

	// The third generation, from the pushed source.
	third := successor(second.bench, "failed-twice-third")
	third.Members = e.template.Spec.Members
	b, err := e.next(third, "failed-twice-third")
	if err != nil {
		t.Fatal("the third generation:", err)
	}
	l := e.ledger()
	retired := func(project uint32) int {
		return len(slices.DeleteFunc(slices.Clone(l.Retired), func(r allocationledger.RetiredBench) bool {
			return r != allocationledger.RetiredBench{Name: "bench:" + key, ProjectID: project}
		}))
	}
	if third.Generation != 3 || b.allocation.ProjectID <= second.allocation.ProjectID || e.row(key) != b.allocation || retired(first.allocation.ProjectID) != 1 || retired(second.allocation.ProjectID) != 1 || l.Fresh["bench:"+key] != b.allocation.ProjectID {
		t.Fatalf("third generation %+v; retired %+v", b.allocation, l.Retired)
	}
	assertBytes(t, filepath.Join(b.allocation.Path, "api", "payload.txt"), []byte("source-api\n"))
	if _, err = os.Lstat(filepath.Join(retirements, payloadbootstrap.Stem(third)+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the third generation has a retirement: %v", err)
	}
	ledgerBytes, err := os.ReadFile(filepath.Join(e.p.Ledger, "assignments.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.next(successor(second.bench, "failed-twice-third-other"), "failed-twice-third-other"); err == nil {
		t.Fatal("a second third generation began")
	}
	assertBytes(t, filepath.Join(e.p.Ledger, "assignments.json"), ledgerBytes)
	t.Log("LOWER_THIRD_GENERATION_AFTER_SECOND_FAILURE_PASS")
	reclaimed, err := e.reclaim(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	e.assertReclaimed(b, reclaimed)
	t.Log("LOWER_THIRD_GENERATION_RECLAIMED_PASS")
}
