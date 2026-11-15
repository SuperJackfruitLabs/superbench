package service

import (
	"context"
	"database/sql"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

// reconcileCancellation remains private and unrouted. The initial independent
// admission calls requestCancelV2; this later operation can wait for a helper or
// host lock without keeping the receipt mutex or an ordinary growth slot held.
func (s *Server) reconcileCancellation(ctx context.Context, t operationcontrol.Target) (string, error) {
	r := s.receipts
	inventory, err := r.CancellationInventory(t)
	if err != nil {
		return "", err
	}
	if inventory.State == "not_admitted" {
		return s.reconcileNoEffects(ctx, t)
	}
	if inventory.State != "cancel_requested" {
		return inventory.State, nil
	}
	i, helpers := inventory.Intent, inventory.Helpers
	manager, err := s.config.Store.OperationCancellation()
	if err != nil {
		return "", err
	}
	if _, err = manager.Inspect(i); errors.Is(err, sql.ErrNoRows) {
		return s.reconcileNoEffects(ctx, t)
	} else if err != nil {
		return "", err
	}
	manager.CheckBarrier = func() error { return r.VerifyCancellationInventory(i, helpers) }
	if err = manager.Reconcile(ctx, i, helpers); err != nil {
		return "", err
	}
	if err = r.ConfirmCancellation(i); err != nil {
		return "", err
	}
	return "cancelled", nil
}

func (s *Server) reconcileNoEffects(ctx context.Context, t operationcontrol.Target) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	snapshot, err := s.receipts.NoEffectsInventory(t)
	if err != nil {
		return "", err
	}
	manager, err := s.config.Store.OperationCancellation()
	if err != nil {
		return "", err
	}
	manager.CheckBarrier = func() error { return s.receipts.VerifyNoEffects(snapshot) }
	if err = manager.NoEffects(t); err != nil {
		return "", err
	}
	if err = s.receipts.ConfirmNoEffects(snapshot); err != nil {
		return "", err
	}
	return "cancelled", nil
}
