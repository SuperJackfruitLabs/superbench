package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

func helperFixture(i operationcontrol.Intent) operationcontrol.Helper {
	h := operationcontrol.Helper{Identity: quotaoperation.Identity{OperationID: i.Target.ID(), Epoch: i.Epoch, Authority: strings.Repeat("c", 64), BinarySHA256: strings.Repeat("d", 64)}, Command: "/fixed/bench-quota", Policy: "/etc/superbench/storage.json", State: "/state", StateDevice: 7, StateInode: 8, Kind: "bench", Key: i.Key, Requested: 1024}
	h.Identity.StateIdentity = h.StateIdentity()
	return h
}
func TestHelperIntentIsDurableBeforeStartAndCannotBeRebound(t *testing.T) {
	root := t.TempDir()
	r, err := openReceipts(root)
	if err != nil {
		t.Fatal(err)
	}
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 8, RequestID: "helper", Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "create", Epoch: strings.Repeat("b", 32)}
	if _, err = r.claim(i.Target.UID, i.Target.RequestID, i.Target.Digest); err != nil {
		t.Fatal(err)
	}
	ctx, closeScope, err := r.operationScope(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	defer closeScope()
	h := helperFixture(i)
	if err = operationcontrol.RecordHelper(ctx, h); err != nil {
		t.Fatal(err)
	}
	got, err := r.operationHelpers(i)
	if err != nil || len(got) != 1 || got[0] != h {
		t.Fatal("helper launch lacked durable original authority", got, err)
	}
	changed := h
	changed.Identity.Authority = strings.Repeat("e", 64)
	if err = operationcontrol.RecordHelper(ctx, changed); err == nil {
		t.Fatal("same resource rebound to new policy/root")
	}
	r.db.Close()
	r, err = openReceipts(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	got, err = r.operationHelpers(i)
	if err != nil || len(got) != 1 || got[0] != h {
		t.Fatal("restart lost helper authority", got, err)
	}
}
func TestCancelledOrIgnoredHelperIntentCannotAuthorizeStart(t *testing.T) {
	for _, mode := range []string{"cancelled", "ignored"} {
		t.Run(mode, func(t *testing.T) {
			r, err := openReceipts(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer r.db.Close()
			i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 9, RequestID: "blocked", Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "setup", Epoch: strings.Repeat("b", 32)}
			if _, err = r.claim(i.Target.UID, i.Target.RequestID, i.Target.Digest); err != nil {
				t.Fatal(err)
			}
			ctx, closeScope, err := r.operationScope(context.Background(), i)
			if err != nil {
				t.Fatal(err)
			}
			defer closeScope()
			if mode == "cancelled" {
				if _, err = r.requestCancelV2(i.Target.UID, i.Target.RequestID, i.Target.Digest); err != nil {
					t.Fatal(err)
				}
			} else if _, err = r.db.Exec(`CREATE TRIGGER ignore_helper BEFORE INSERT ON operation_helpers BEGIN SELECT RAISE(IGNORE); END`); err != nil {
				t.Fatal(err)
			}
			if err = operationcontrol.RecordHelper(ctx, helperFixture(i)); err == nil {
				t.Fatal("uncommitted helper intent acknowledged")
			}
			var count int
			if err = r.db.QueryRow(`SELECT count(*) FROM operation_helpers`).Scan(&count); err != nil || count != 0 {
				t.Fatal(count, err)
			}
		})
	}
}
func TestLostReceiptAndIntentCannotReopenRetainedHelperAuthority(t *testing.T) {
	root := t.TempDir()
	r, err := openReceipts(root)
	if err != nil {
		t.Fatal(err)
	}
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 7, RequestID: "lost-helper", Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "run", Epoch: strings.Repeat("b", 32)}
	if _, err = r.claim(i.Target.UID, i.Target.RequestID, i.Target.Digest); err != nil {
		t.Fatal(err)
	}
	ctx, closeScope, err := r.operationScope(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	defer closeScope()
	if err = operationcontrol.RecordHelper(ctx, helperFixture(i)); err != nil {
		t.Fatal(err)
	}
	if _, err = r.db.Exec(`DELETE FROM operation_intents;DELETE FROM receipts`); err != nil {
		t.Fatal(err)
	}
	r.db.Close()
	r, err = openReceipts(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	if _, err = r.claim(i.Target.UID, i.Target.RequestID, i.Target.Digest); !errors.Is(err, errInDoubt) {
		t.Fatal("lost helper receipt redispatched", err)
	}
	if state, err := r.requestCancelV2(i.Target.UID, i.Target.RequestID, i.Target.Digest); !errors.Is(err, errInDoubt) || state != "" {
		t.Fatal("unbound helper adopted as no effects", state, err)
	}
}

func TestHelperSchemaUpgradePreservesHistoricalReceiptAndRejectsDowngrade(t *testing.T) {
	root := t.TempDir()
	r, err := openReceipts(root)
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	if _, err = r.claim(1, "done", digest); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"historical":"unchanged"}`)
	if err = r.finish(1, "done", body, 200); err != nil {
		t.Fatal(err)
	}
	if _, err = r.db.Exec(`DROP TABLE operation_helpers;ALTER TABLE operation_intents DROP COLUMN core_finalized;ALTER TABLE operation_intents DROP COLUMN helper_tracking;PRAGMA user_version=3`); err != nil {
		t.Fatal(err)
	}
	r.db.Close()
	r, err = openReceipts(root)
	if err != nil {
		t.Fatal("legitimate3 failed upgrade", err)
	}
	got, _, err := r.lookup(1, "done", digest)
	if err != nil || got == nil || string(got.body) != string(body) {
		t.Fatal("schema upgrade changed historical result", got, err)
	}
	var count int
	if err = r.db.QueryRow(`SELECT count(*) FROM operation_helpers`).Scan(&count); err != nil || count != 0 {
		t.Fatal("schema invented helper authority", count, err)
	}
	if _, err = r.db.Exec(`PRAGMA user_version=3`); err != nil {
		t.Fatal(err)
	}
	r.db.Close()
	if later, err := openReceipts(root); err == nil {
		later.db.Close()
		t.Fatal("future helper authority adopted from older schema")
	}
}

func TestHelperIntentInventoryIsClosedAndExistingResourcesRemainIdempotent(t *testing.T) {
	r, err := openReceipts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 3, RequestID: "resources", Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "create", Epoch: strings.Repeat("b", 32)}
	if _, err = r.claim(i.Target.UID, i.Target.RequestID, i.Target.Digest); err != nil {
		t.Fatal(err)
	}
	ctx, closeScope, err := r.operationScope(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	defer closeScope()
	bench := helperFixture(i)
	for _, key := range []string{"store", "mirrors"} {
		h := bench
		h.Kind = "shared"
		h.Key = key
		h.Requested = 0
		if err = operationcontrol.RecordHelper(ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	if err = operationcontrol.RecordHelper(ctx, bench); err != nil {
		t.Fatal(err)
	}
	if err = operationcontrol.RecordHelper(ctx, bench); err != nil {
		t.Fatal("existing resource failed at bound", err)
	}
	invalid := bench
	invalid.Kind = "shared"
	invalid.Key = "unknown"
	invalid.Requested = 0
	if err = operationcontrol.RecordHelper(ctx, invalid); err == nil {
		t.Fatal("unknown fixed resource accepted")
	}
	changed := bench
	changed.Requested *= 2
	if err = operationcontrol.RecordHelper(ctx, changed); err == nil {
		t.Fatal("original requested cap was replaced")
	}
	got, err := r.operationHelpers(i)
	if err != nil || len(got) != 3 {
		t.Fatal(got, err)
	}
	wrong := i
	wrong.Epoch = strings.Repeat("c", 32)
	if _, err = r.operationHelpers(wrong); err == nil {
		t.Fatal("other epoch adopted invocation inventory")
	}
}
