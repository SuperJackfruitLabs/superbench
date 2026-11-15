package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

func TestPrivateReconciliationProvesPreAdmissionCancellationWithoutCreatingWorkspace(t *testing.T) {
	for _, typed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "typed-before-core"}[typed], func(t *testing.T) {
			s, _, _ := v2Fixture(t)
			i := operationcontrol.Intent{Target: operationcontrol.Target{UID: v2Operator, RequestID: "before-admission", Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "create", Epoch: strings.Repeat("b", 32)}
			if typed {
				_, _, stop, err := s.receipts.ClaimScope(context.Background(), i)
				if err != nil {
					t.Fatal(err)
				}
				defer stop()
			}
			if state, err := s.receipts.RequestCancel(i.Target.UID, i.Target.RequestID, i.Target.Digest); err != nil || state != "cancel_requested" {
				t.Fatal(state, err)
			}
			state, err := s.reconcileCancellation(context.Background(), i.Target)
			if err != nil || state != "cancelled" {
				t.Fatal("valid no-effect target stranded", state, err)
			}
			if rows := v2Snapshot(t, s); len(rows) != 0 {
				t.Fatal("no-effect reconciliation created workspace", rows)
			}
			if _, err = s.receipts.Claim(i.Target.UID, i.Target.RequestID, i.Target.Digest); !errors.Is(err, errCancelRequested) {
				t.Fatal("cancelled target became executable", err)
			}
			if state, err = s.reconcileCancellation(context.Background(), i.Target); err != nil || state != "cancelled" {
				t.Fatal("terminal replay changed", state, err)
			}
		})
	}
}

func TestPrivateRestartWorkerCompletesDurableNoEffectTombstone(t *testing.T) {
	s, _, _ := v2Fixture(t)
	target := operationcontrol.Target{UID: v2Operator, RequestID: "restart-unadmitted", Digest: strings.Repeat("a", 64)}
	if _, err := s.receipts.RequestCancel(target.UID, target.RequestID, target.Digest); err != nil {
		t.Fatal(err)
	}
	if err := s.receipts.Close(); err != nil {
		t.Fatal(err)
	}
	replacement, err := openReceipts(s.config.Store.State)
	if err != nil {
		t.Fatal(err)
	}
	s.receipts = replacement
	t.Cleanup(func() { _ = replacement.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.runOperationReconciler(ctx, nil, nil) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("restart worker not joined")
		}
	}()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		state, err := s.receipts.RequestCancel(target.UID, target.RequestID, target.Digest)
		if err != nil {
			t.Fatal(err)
		}
		if state == "cancelled" {
			break
		}
		if state != "cancel_requested" {
			t.Fatal(state)
		}
		select {
		case <-deadline.C:
			t.Fatal("owned no-effect tombstone stranded after restart")
		case <-tick.C:
		}
	}
	if rows := v2Snapshot(t, s); len(rows) != 0 {
		t.Fatal("no-effect restart created workspace", rows)
	}
}
