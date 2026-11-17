package receiptlimittest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/finiteio"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationreceipts"
)

func TestRefusedFirstFiniteClaimRollsBackTheFormatUpgrade(t *testing.T) {
	state := t.TempDir()
	ledger := open(t, state)
	defer ledger.Close()
	// 1024 unresolved legacy identities fill the 64 MiB budget in format 6.
	for i := 0; i < 1024; i++ {
		if _, err := ledger.Claim(1, fmt.Sprintf("legacy-%d", i), digest(i)); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := ledger.ClaimLimit(1, "finite", digest(0), finiteLimit); !errors.Is(err, operationreceipts.ErrCapacity) {
		t.Fatal("first finite claim not refused", err)
	}
	if version, tables := format(t, state); version != 6 || tables != 0 {
		t.Fatal("refused claim left a format-7 upgrade behind", version, tables)
	}
	if err := ledger.Finish(1, "legacy-0", body(64), 200); err != nil {
		t.Fatal(err)
	}
	// Only an exact format-6 ledger may be upgraded.
	if _, err := raw(t, state).Exec(`PRAGMA user_version=5`); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.ClaimLimit(1, "finite", digest(0), finiteLimit); !errors.Is(err, operationreceipts.ErrInDoubt) {
		t.Fatal("non-format-6 ledger upgraded", err)
	}
	if version, tables := format(t, state); version != 5 || tables != 0 {
		t.Fatal("refused upgrade changed the ledger", version, tables)
	}
}

func TestFiniteTypedClaimReservesItsBoundAndReplaysOnlyIt(t *testing.T) {
	state := t.TempDir()
	ledger := open(t, state)
	defer ledger.Close()
	intent := func(n int) operationcontrol.Intent {
		return operationcontrol.Intent{Target: operationcontrol.Target{UID: 1, RequestID: fmt.Sprintf("run-%d", n), Digest: digest(n)}, Key: "card", Operation: "run", Epoch: strings.Repeat("e", 32)}
	}
	for i := 0; i < 32; i++ {
		cached, _, stop, err := ledger.ClaimScopeLimit(context.Background(), intent(i), finiteio.MaxBody)
		if err != nil || cached != nil || stop == nil {
			t.Fatal(i, err)
		}
		defer stop()
	}
	if version, tables := format(t, state); version != 7 || tables != 1 {
		t.Fatal("typed finite claim did not record its bound", version, tables)
	}
	if _, _, _, err := ledger.ClaimScopeLimit(context.Background(), intent(99), operationreceipts.LegacyResponseLimit); !errors.Is(err, operationreceipts.ErrCapacity) {
		t.Fatal("typed finite reservations not charged", err)
	}
	if _, _, _, err := ledger.ClaimScopeLimit(context.Background(), intent(100), finiteio.MaxBody+1); err == nil {
		t.Fatal("typed claim accepted an invalid bound")
	}
	want := body(finiteLimit)
	if err := ledger.Finish(1, "run-0", want, 422); err != nil {
		t.Fatal(err)
	}
	cached, _, _, err := ledger.ClaimScopeLimit(context.Background(), intent(0), finiteio.MaxBody)
	if err != nil || cached == nil || string(cached.Bytes()) != string(want) {
		t.Fatal("typed finite receipt did not replay", err)
	}
}
