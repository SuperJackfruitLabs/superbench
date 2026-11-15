package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationreceipts"
)

func TestRestartWorkersDoNotHoldCancellationAdmission(t *testing.T) {
	r, err := openReceipts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for n := 0; n < 8; n++ {
		i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 9, RequestID: fmt.Sprintf("waiting-%d", n), Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "lease", Epoch: strings.Repeat("b", 32)}
		if _, err = r.Claim(i.Target.UID, i.Target.RequestID, i.Target.Digest); err != nil {
			t.Fatal(err)
		}
		_, stop, e := r.Scope(context.Background(), i)
		if e != nil {
			t.Fatal(e)
		}
		stop()
		if _, err = r.RequestCancel(i.Target.UID, i.Target.RequestID, i.Target.Digest); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{}, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runReconciliation(ctx, r, nil, func(ctx context.Context, _ operationreceipts.ReconciliationWork) error {
			started <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		}, nil)
	}()
	for n := 0; n < 4; n++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("fixed workers did not start")
		}
	}
	select {
	case <-started:
		t.Fatal("more than four concurrent reconciliation waits")
	default:
	}
	ack := make(chan error, 1)
	go func() {
		state, err := r.RequestCancel(19, "independent", strings.Repeat("c", 64))
		if err == nil && state != "cancel_requested" {
			err = fmt.Errorf("unexpected %s", state)
		}
		ack <- err
	}()
	select {
	case err := <-ack:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("helper waits held receipt admission")
	}
	cancel()
	select {
	case <-done:
		state, err := r.RequestCancel(9, "waiting-0", strings.Repeat("a", 64))
		if err != nil || state != "cancel_requested" {
			t.Fatal("worker exit fabricated terminal proof", state, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("workers not joined")
	}
}
