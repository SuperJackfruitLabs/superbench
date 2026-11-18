//go:build linux && freshstoragefixture

package fixture

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/allocationledger"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshrecovery"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

// failedRecreates proves on real XFS that a Fresh create which failed while
// materializing, once root recovers it by the documented direct-root steps
// (deny its operation, clean, remove the empty leaf, retire), gives way to
// its key's next generation: root's read-only inspection proves the
// retirement, the next generation takes a new project in place of the
// tombstone, and the old project is never revived. The new generation is
// then reclaimed, so the case leaves no reservation behind.
func (e *env) failedRecreates() {
	t := e.t
	ctx := t.Context()
	key := "failed-create"
	s := e.template.Spec
	s.Key, s.ID, s.QuotaBytes = key, hash("failed-bid:" + key)[:32], 0
	// A source commit the mirror does not hold: Materialize records staging,
	// then its first check of the member fails, as a real failed create does.
	s.Members = append([]payloadbootstrap.Member{}, s.Members...)
	s.Members[0].Source = strings.Repeat("e", len(s.Members[0].Source))
	identity := e.identity("create:" + key)
	store, err := e.control.Operation(ctx, identity, func(i quotaoperation.Identity) error {
		authority, err := e.p.OperationAuthority(ctx)
		if err != nil {
			return err
		}
		if i != identity || i.Authority != authority {
			return quotaoperation.ErrAuthority
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := payloadbootstrap.Open(ctx, e.control, identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Bootstrap(ctx, s); err == nil {
		t.Fatal("a create with an absent source commit completed")
	}
	i, err := m.Inspect(ctx, s)
	if err != nil || i.Phase != "staging" || i.OperationDenied || i.RecordedAllocation.ProjectID < 10000 {
		t.Fatalf("failed create: %+v %v", i, err)
	}
	failed := bench{s, hoststorage.Assignment(i.RecordedAllocation)}
	second := successor(failed, "failed-second")
	second.Members = e.template.Spec.Members

	// Before root's recovery: root's inspection and the next generation refuse.
	bootstrapDir := filepath.Join(e.p.Ledger, "payload-bootstrap")
	row := e.row(key)
	if _, err = freshrecovery.InspectRetired(ctx, s); err == nil {
		t.Fatal("root proved an unretired create retired")
	}
	if _, err = e.next(second, "failed-second-early"); err == nil {
		t.Fatal("a next generation began before root retired the failed create")
	}
	if _, err = os.Lstat(filepath.Join(bootstrapDir, payloadbootstrap.Stem(second)+".json")); !errors.Is(err, os.ErrNotExist) || e.row(key) != row {
		t.Fatalf("a refused next generation had effects: %v %+v", err, e.row(key))
	}
	t.Log("LOWER_FAILED_CREATE_REFUSED_BEFORE_RETIREMENT_PASS")

	// Root's recovery: deny the original operation, then clean, remove the
	// empty leaf and retire, as fresh-bootstrap-recovery.md documents.
	if _, err = store.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	l := &leak{spec: s, manager: m, a: failed.allocation, gateHits: new(int)}
	l.intent.Root = node{i.RecordedRoot.Device, i.RecordedRoot.Inode}
	hits := l.gateHits
	e.gate(m, func(context.Context, hoststorage.Policy) error { *hits++; return nil })
	failedIntent, err := os.ReadFile(filepath.Join(bootstrapDir, key+".json"))
	if err != nil {
		t.Fatal(err)
	}
	e.recoverDaemon(l)
	if _, err = os.Lstat(filepath.Join(e.p.Ledger, "payload-retirements", key+".json")); err != nil {
		t.Fatal("no retained retirement:", err)
	}
	assertBytes(t, filepath.Join(bootstrapDir, key+".json"), failedIntent)
	if p := e.intentPhase(key); p != "staging" {
		t.Fatalf("the retired intent was rewritten to %s", p)
	}
	tombstone := failed.allocation
	tombstone.Bytes, tombstone.Inodes = 0, 0
	got, err := freshrecovery.InspectRetired(ctx, s)
	if err != nil || got != (quotahelper.RetiredResult{Version: quotahelper.RetiredVersion, Key: key, ID: s.ID, Allocation: tombstone}) {
		t.Fatalf("root's inspection of the retired create: %+v %v", got, err)
	}
	if err = got.Validate(e.p.Root, key, s.ID, 0); err != nil {
		t.Fatal("the daemon would refuse root's reply:", err)
	}
	other := s
	other.ID = hash("failed-bid:other")[:32]
	if _, err = freshrecovery.InspectRetired(ctx, other); err == nil {
		t.Fatal("root proved another workspace retired")
	}
	t.Log("LOWER_FAILED_CREATE_ROOT_PROVES_RETIRED_PASS")

	// The next generation: a new project in place of the tombstone, from the
	// pushed source, and the old project retired for good.
	b, err := e.next(second, "failed-second")
	if err != nil {
		t.Fatal("the next generation:", err)
	}
	ledger := e.ledger()
	wantRetired := allocationledger.RetiredBench{Name: "bench:" + key, ProjectID: failed.allocation.ProjectID}
	retired := 0
	for _, r := range ledger.Retired {
		if r == wantRetired {
			retired++
		}
	}
	if b.allocation.Path != failed.allocation.Path || b.allocation.ProjectID <= failed.allocation.ProjectID || b.allocation.Bytes == 0 || e.row(key) != b.allocation || retired != 1 || ledger.Fresh["bench:"+key] != b.allocation.ProjectID {
		t.Fatalf("next generation allocation %+v after %+v; retired %+v", b.allocation, failed.allocation, ledger.Retired)
	}
	assertBytes(t, filepath.Join(b.allocation.Path, "api", "payload.txt"), []byte("source-api\n"))
	if p := e.intentPhase(payloadbootstrap.Stem(second)); p != "completed" {
		t.Fatalf("next generation intent %s", p)
	}
	t.Logf("failed project %d, next project %d", failed.allocation.ProjectID, b.allocation.ProjectID)
	t.Log("LOWER_FAILED_CREATE_NEXT_GENERATION_NEW_PROJECT_PASS")

	// Neither the failed create nor a second successor of it can be made,
	// and root no longer reports the failed create retired-and-free.
	ledgerBytes, err := os.ReadFile(filepath.Join(e.p.Ledger, "assignments.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.next(s, "failed-again"); err == nil {
		t.Fatal("the failed create was made again")
	}
	if _, err = e.next(successor(bench{s, failed.allocation}, "failed-second-other"), "failed-second-other"); err == nil {
		t.Fatal("a second successor of the failed create began")
	}
	if _, err = freshrecovery.InspectRetired(ctx, s); err == nil {
		t.Fatal("root reported a superseded tombstone")
	}
	assertBytes(t, filepath.Join(e.p.Ledger, "assignments.json"), ledgerBytes)
	assertBytes(t, filepath.Join(bootstrapDir, key+".json"), failedIntent)
	t.Log("LOWER_FAILED_CREATE_OLD_PROJECT_NOT_REVIVED_PASS")

	// Return the capacity: the next generation reclaims like any other.
	reclaimed, err := e.reclaim(ctx, b)
	if err != nil {
		t.Fatal("next generation reclaim:", err)
	}
	e.assertReclaimed(b, reclaimed)
	if l := e.ledger(); !reflect.DeepEqual(l.Retired, ledger.Retired) {
		t.Fatalf("retired generations after the reclaim: %+v", l.Retired)
	}
	t.Log("LOWER_FAILED_CREATE_NEXT_GENERATION_RECLAIMED_PASS")
}
