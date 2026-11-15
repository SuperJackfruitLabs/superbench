package operationcancel

import (
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"testing"
)

func TestNormalFinalizationRequiresDurableCutoffAndLeavesOwnerUntouched(t *testing.T) {
	m, db, i, b, _ := fixture(t)
	token, continuation, stopped := b.Lease.Token, b.Lease.Continuation, b.Lease.StopRequested
	denied := errors.New("receipt not finalized")
	if err := m.Finish(i, func() error { return denied }); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	before, err := operationcontrol.Get(db, i.Target)
	if err != nil || before.Phase != "bound" {
		t.Fatal(before, err)
	}
	if err = m.Finish(i, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	after, err := operationcontrol.Get(db, i.Target)
	if err != nil || after.Phase != "finished" {
		t.Fatal(after, err)
	}
	if b.Lease.Token != token || b.Lease.Continuation != continuation || b.Lease.StopRequested != stopped {
		t.Fatal("normal cutoff changed owner", b)
	}
}
