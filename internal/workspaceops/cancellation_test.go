package workspaceops

import (
	"context"
	"errors"
	"testing"
)

func TestCancellationAfterRawAcquisitionSynchronouslyStopsAndPreservesGrant(t *testing.T) {
	m, r := fixture(t)
	create(t, m, r)
	held, err := m.Store.AcquireWorkspaceOwnerContext(context.Background(), "card", "worker")
	if err != nil {
		t.Fatal(err)
	}
	token := held.Lease.Token
	out := result(held, token, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.reconcileCanceledWork(ctx, request("setup"), token, token, &out, &err)
	if !errors.Is(err, context.Canceled) || out == nil || out.GrantToken != token || !out.Workspace.Lease.StopRequested || out.Workspace.Lease.Continuation != "" || out.Workspace.Lease.Token != "" {
		t.Fatal("canceled acquisition lost stopped owned grant", err)
	}
	rows := snapshot(t, m)
	if !rows[0].Lease.StopRequested {
		t.Fatal("cancellation returned before durable stop")
	}
	setup := request("setup")
	setup.LeaseToken = token
	if _, err := m.Execute(context.Background(), setup, 42); err == nil {
		t.Fatal("canceled grant automatically resumed setup")
	}
}

func TestLateCancellationAfterReleaseCannotStopDifferentOwner(t *testing.T) {
	m, r := fixture(t)
	create(t, m, r)
	held, err := m.Store.AcquireWorkspaceOwnerContext(context.Background(), "card", "worker")
	if err != nil {
		t.Fatal(err)
	}
	oldToken := held.Lease.Token
	if _, err := m.Store.ReleaseWorkspaceContext(context.Background(), "card", oldToken); err != nil {
		t.Fatal(err)
	}
	next, err := m.Store.AcquireWorkspaceOwnerContext(context.Background(), "card", "worker")
	if err != nil {
		t.Fatal(err)
	}
	out := result(held, "", false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.reconcileCanceledWork(ctx, request("setup"), oldToken, oldToken, &out, &err)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation was lost", err)
	}
	rows := snapshot(t, m)
	if rows[0].Lease.StopRequested {
		t.Fatal("late cancellation stopped replacement owner")
	}
	if err := m.Store.CheckWorkspaceOwner("card", next.Lease.Token, "worker"); err != nil {
		t.Fatal("late cancellation changed replacement token", err)
	}
}
