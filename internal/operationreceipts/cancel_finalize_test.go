package operationreceipts

import (
	"context"
	"errors"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

func TestFinalizationCutoffRequiresExactHistoricalReceipt(t *testing.T) {
	r, err := openReceipts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 9, RequestID: "cutoff", Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "lease", Epoch: strings.Repeat("b", 32)}
	if _, err = r.claim(i.Target.UID, i.Target.RequestID, i.Target.Digest); err != nil {
		t.Fatal(err)
	}
	_, stop, err := r.operationScope(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if err = r.finalizationCutoff(i); !errors.Is(err, errInDoubt) {
		t.Fatal("inflight operation considered complete", err)
	}
	original := []byte(`{"original":"historical"}`)
	if err = r.finish(i.Target.UID, i.Target.RequestID, original, 200); err != nil {
		t.Fatal(err)
	}
	if err = r.finalizationCutoff(i); err != nil {
		t.Fatal(err)
	}
	wrong := i
	wrong.Epoch = strings.Repeat("c", 32)
	if err = r.finalizationCutoff(wrong); err == nil {
		t.Fatal("wrong operation epoch inherited cutoff")
	}
	if state, err := r.requestCancelV2(i.Target.UID, i.Target.RequestID, i.Target.Digest); err != nil || state != "already_completed" {
		t.Fatal(state, err)
	}
	got, _, err := r.lookup(i.Target.UID, i.Target.RequestID, i.Target.Digest)
	if err != nil || got == nil || string(got.body) != string(original) {
		t.Fatal("historical response changed", got, err)
	}
}
