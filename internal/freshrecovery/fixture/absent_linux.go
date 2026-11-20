//go:build linux && freshstoragefixture

package fixture

import (
	"errors"
	"os"
	"path/filepath"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshrecovery"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
)

// capacityRefusedAbsent proves on real XFS that a create refused for
// capacity left root nothing: no intent, no row and no leaf, so root's
// retired inspection has nothing to answer for and its absence inspection
// proves the key free of that create, writing nothing. A key that holds an
// allocation, and a later generation whose predecessor root never retired,
// are not absent. live is a completed bench on the same volume.
func (e *env) capacityRefusedAbsent(s payloadbootstrap.Spec, live bench) {
	t := e.t
	ctx := t.Context()
	ledger := filepath.Join(e.p.Ledger, "assignments.json")
	before, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	bootstrapDir := filepath.Join(e.p.Ledger, "payload-bootstrap")
	stem := payloadbootstrap.Stem(s)
	for _, path := range []string{filepath.Join(bootstrapDir, stem+".json"), filepath.Join(bootstrapDir, stem+".controls"), filepath.Join(e.p.Root, "benches", s.Key)} {
		if _, err = os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the refused create left %s: %v", path, err)
		}
	}
	if _, ok := e.ledger().Assignments["bench:"+s.Key]; ok {
		t.Fatal("the refused create left a ledger row")
	}
	if _, err = freshrecovery.InspectRetired(ctx, s); err == nil {
		t.Fatal("root proved a create that reserved nothing retired")
	}
	got, err := freshrecovery.InspectAbsent(ctx, s)
	if err != nil || got != (quotahelper.AbsentResult{Version: quotahelper.AbsentVersion, Key: s.Key, ID: s.ID}) {
		t.Fatalf("root's absence inspection: %+v %v", got, err)
	}
	if err = got.Validate(s.Key, s.ID, 0); err != nil {
		t.Fatal("the daemon would refuse root's reply:", err)
	}
	if _, err = freshrecovery.InspectAbsent(ctx, live.spec); err == nil {
		t.Fatal("root proved a key with a live allocation absent")
	}
	later := live.spec
	later.Version, later.Generation, later.Previous, later.ID = payloadbootstrap.GenerationVersion, 2, live.spec.ID, hash("absent-bid:later")[:32]
	if _, err = freshrecovery.InspectAbsent(ctx, later); err == nil {
		t.Fatal("root proved a later generation absent over a live predecessor")
	}
	assertBytes(t, ledger, before)
	t.Log("LOWER_CAPACITY_REFUSED_CREATE_ROOT_PROVES_ABSENT_PASS")
}
