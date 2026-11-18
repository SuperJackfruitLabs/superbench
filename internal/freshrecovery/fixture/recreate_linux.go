//go:build linux && freshstoragefixture

package fixture

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/allocationledger"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacebootstrap"
)

// successor is the next generation's spec after b: a new BID naming b's.
func successor(b bench, label string) payloadbootstrap.Spec {
	s := b.spec
	s.Version, s.ID, s.Previous = payloadbootstrap.GenerationVersion, hash("recreate-bid:" + label)[:32], b.spec.ID
	s.Generation = 2
	if b.spec.Generation != 0 {
		s.Generation = b.spec.Generation + 1
	}
	return s
}

func (e *env) next(s payloadbootstrap.Spec, label string) (bench, error) {
	m, err := payloadbootstrap.Open(e.t.Context(), e.control, e.identity("create:"+label))
	if err != nil {
		e.t.Fatal(err)
	}
	result, err := m.Bootstrap(e.t.Context(), s)
	return bench{s, result.Allocation}, err
}

func (e *env) ledger() allocationledger.Ledger {
	e.t.Helper()
	var l allocationledger.Ledger
	raw, err := os.ReadFile(filepath.Join(e.p.Ledger, "assignments.json"))
	if err != nil || json.Unmarshal(raw, &l) != nil {
		e.t.Fatal("ledger", err)
	}
	return l
}

func inode(t interface{ Fatal(...any) }, path string) [2]uint64 {
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	st := info.Sys().(*syscall.Stat_t)
	return [2]uint64{uint64(st.Dev), st.Ino}
}

// recreates proves a reclaimed key's next generation on real XFS: root refuses
// while the old layout is published, the daemon's move aside, a new project
// and a fresh tree from the pushed branch, the old evidence kept, the old
// project never revived, and the second generation's own reclaim.
func (e *env) recreates() {
	t := e.t
	key := "recreate"
	first, err := e.bootstrap(key)
	if err != nil {
		t.Fatal(err)
	}
	e.payloadShell(first.allocation.Path, `printf gen1 > api/first-generation-only`)
	got, err := e.reclaim(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	e.assertReclaimed(first, got)
	bootstrapDir := filepath.Join(e.p.Ledger, "payload-bootstrap")
	firstIntent, err := os.ReadFile(filepath.Join(bootstrapDir, key+".json"))
	if err != nil {
		t.Fatal(err)
	}
	tombstone := e.row(key)

	// The first generation's layout still holds the key's name: root refuses
	// before any effect.
	policy, err := payloadexec.LoadFixedPolicy()
	if err != nil {
		t.Fatal(err)
	}
	second := successor(first, "second")
	if _, err = e.next(second, "recreate-refused"); err == nil {
		t.Fatal("a next generation began while the old layout was published")
	}
	if _, err = os.Lstat(filepath.Join(bootstrapDir, payloadbootstrap.Stem(second)+".json")); !errors.Is(err, os.ErrNotExist) || e.row(key) != tombstone {
		t.Fatalf("a refused next generation had effects: %v %+v", err, e.row(key))
	}
	t.Log("LOWER_RECREATE_REFUSED_WHILE_OLD_LAYOUT_PUBLISHED_PASS")

	// The daemon moves it aside, as its create does before it asks.
	record := &model.Bench{Key: key, Workspace: &model.Workspace{ID: second.ID, Generation: second.Generation, Previous: second.Previous}}
	if err = workspacebootstrap.MoveLayoutAside(policy.StateRoot, record); err != nil {
		t.Fatal(err)
	}
	if err = workspacebootstrap.MoveLayoutAside(policy.StateRoot, record); err != nil {
		t.Fatal("a repeated move aside:", err)
	}
	e.guardsBeforeTheNextGeneration(first, firstIntent)
	b, err := e.next(second, "recreate")
	if err != nil {
		t.Fatal("the next generation:", err)
	}
	l := e.ledger()
	wantRetired := []allocationledger.RetiredBench{{Name: "bench:" + key, ProjectID: first.allocation.ProjectID}}
	if b.allocation.Path != first.allocation.Path || b.allocation.ProjectID <= first.allocation.ProjectID || b.allocation.Bytes == 0 || e.row(key) != b.allocation ||
		l.FormatVersion != allocationledger.FormatGenerations || !reflect.DeepEqual(l.Retired, wantRetired) || l.Fresh["bench:"+key] != b.allocation.ProjectID {
		t.Fatalf("next generation allocation %+v after %+v; ledger %+v", b.allocation, first.allocation, l.Retired)
	}
	t.Logf("first project %d, second project %d", first.allocation.ProjectID, b.allocation.ProjectID)
	t.Log("LOWER_RECREATE_NEW_PROJECT_PASS")

	// A fresh tree, materialized from the source vector: nothing of the first.
	assertBytes(t, filepath.Join(b.allocation.Path, "api", "payload.txt"), []byte("source-api\n"))
	if _, err = os.Lstat(filepath.Join(b.allocation.Path, "api", "first-generation-only")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the first generation's file is in the second's tree: %v", err)
	}
	if p := e.intentPhase(payloadbootstrap.Stem(second)); p != "completed" {
		t.Fatalf("second generation intent %s", p)
	}
	t.Log("LOWER_RECREATE_FRESH_TREE_PASS")

	// The first generation's intent, controls and layout stay as evidence.
	assertBytes(t, filepath.Join(bootstrapDir, key+".json"), firstIntent)
	if inode(t, filepath.Join(bootstrapDir, key+".controls")) == inode(t, filepath.Join(bootstrapDir, payloadbootstrap.Stem(second)+".controls")) {
		t.Fatal("the generations share controls")
	}
	layouts := filepath.Join(policy.StateRoot, "payload-layouts")
	var named struct{ Key, ID string }
	raw, err := os.ReadFile(filepath.Join(layouts, key+".json"))
	if err != nil || json.Unmarshal(raw, &named) != nil || named.ID != second.ID {
		t.Fatalf("published layout %s: %v", raw, err)
	}
	if _, err = os.Lstat(filepath.Join(layouts, key+"."+first.spec.ID+".reclaimed")); err != nil {
		t.Fatal("the first layout was not kept:", err)
	}
	t.Log("LOWER_RECREATE_OLD_EVIDENCE_RETAINED_PASS")
	e.secondSuccessorRefused(first, b, layouts)

	// Neither the first generation nor another second one can be made again.
	ledgerBytes, err := os.ReadFile(filepath.Join(e.p.Ledger, "assignments.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.next(first.spec, "recreate-first-again"); err == nil {
		t.Fatal("the first generation was created again")
	}
	other := successor(first, "second-other")
	if _, err = e.next(other, "recreate-second-again"); err == nil {
		t.Fatal("a second next generation of the same predecessor began")
	}
	assertBytes(t, filepath.Join(e.p.Ledger, "assignments.json"), ledgerBytes)
	t.Log("LOWER_RECREATE_OLD_PROJECT_NOT_REVIVED_PASS")

	// The second generation reclaims like the first, by its own controls.
	got, err = e.reclaim(t.Context(), b)
	if err != nil {
		t.Fatal("second generation reclaim:", err)
	}
	e.assertReclaimed(b, got)
	if l = e.ledger(); !reflect.DeepEqual(l.Retired, wantRetired) {
		t.Fatalf("retired generations after the second reclaim: %+v", l.Retired)
	}
	t.Log("LOWER_RECREATE_SECOND_GENERATION_RECLAIMED_PASS")
}
