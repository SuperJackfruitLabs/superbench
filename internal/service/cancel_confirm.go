package service

import (
	"context"
	"database/sql"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

// confirmCancellation is called only after independent core reconciliation has
// proved inactivity, actual helper denies and sealed/quiescent owned histories.
// This database transition itself cannot provide those proofs.
func (r *receipts) confirmCancellation(i operationcontrol.Intent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, err := r.operationIntent(i.Target)
	if err != nil {
		return err
	}
	if stored != i {
		return errConflict
	}
	if err = r.requireHelperTracking(i); err != nil {
		return err
	}
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var digest, state, barrier string
	if err = tx.QueryRow(`SELECT digest,state,cancel_state FROM receipts WHERE owner=? AND id=?`, i.Target.UID, i.Target.RequestID).Scan(&digest, &state, &barrier); err != nil {
		return err
	}
	if digest != i.Target.Digest {
		return errConflict
	}
	if state != "running" && state != "interrupted" {
		return errInDoubt
	}
	if barrier != "cancel_requested" && barrier != "cancelled" {
		return errInDoubt
	}
	result, err := tx.Exec(`UPDATE receipts SET state='interrupted',cancel_state='cancelled',body=NULL,status=NULL WHERE owner=? AND id=? AND digest=? AND state IN ('running','interrupted') AND cancel_state IN ('cancel_requested','cancelled')`, i.Target.UID, i.Target.RequestID, i.Target.Digest)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errInDoubt
	}
	if err = tx.QueryRow(`SELECT state,cancel_state FROM receipts WHERE owner=? AND id=?`, i.Target.UID, i.Target.RequestID).Scan(&state, &barrier); err != nil {
		return err
	}
	if state != "interrupted" || barrier != "cancelled" {
		return errInDoubt
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	delete(r.active, receiptKey(i.Target.UID, i.Target.RequestID))
	return nil
}

// reconcileCancellation remains private and unrouted. The initial independent
// admission calls requestCancelV2; this later operation can wait for a helper or
// host lock without keeping the receipt mutex or an ordinary growth slot held.
func (s *Server) reconcileCancellation(ctx context.Context, t operationcontrol.Target) (string, error) {
	r := s.receipts
	r.mu.Lock()
	var state, barrier, digest string
	err := r.db.QueryRow(`SELECT state,cancel_state,digest FROM receipts WHERE owner=? AND id=?`, t.UID, t.RequestID).Scan(&state, &barrier, &digest)
	if err != nil {
		r.mu.Unlock()
		if err == sql.ErrNoRows {
			return "", errInDoubt
		}
		return "", err
	}
	if digest != t.Digest {
		r.mu.Unlock()
		return "", errConflict
	}
	if state == "done" {
		r.mu.Unlock()
		return "already_completed", nil
	}
	if barrier == "cancelled" {
		r.mu.Unlock()
		return "cancelled", nil
	}
	if barrier != "cancel_requested" {
		r.mu.Unlock()
		return "", errInDoubt
	}
	i, err := r.operationIntent(t)
	var helpers []operationcontrol.Helper
	if err == nil {
		helpers, err = r.operationHelpers(i)
	}
	r.mu.Unlock()
	if err != nil {
		return "", err
	}
	manager, err := s.config.Store.OperationCancellation()
	if err != nil {
		return "", err
	}
	manager.CheckBarrier = func() error { return r.verifyCancellationInventory(i, helpers) }
	if err = manager.Reconcile(ctx, i, helpers); err != nil {
		return "", err
	}
	if err = r.confirmCancellation(i); err != nil {
		return "", err
	}
	return "cancelled", nil
}

// Verify the exact immutable inventory again after privileged waits and before
// the terminal core commit; no recorder can widen it after cancel admission.
func (r *receipts) verifyCancellationInventory(i operationcontrol.Intent, expected []operationcontrol.Helper) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var digest, state, barrier string
	if err := r.db.QueryRow(`SELECT digest,state,cancel_state FROM receipts WHERE owner=? AND id=?`, i.Target.UID, i.Target.RequestID).Scan(&digest, &state, &barrier); err != nil {
		return err
	}
	if digest != i.Target.Digest {
		return errConflict
	}
	if (state != "running" && state != "interrupted") || barrier != "cancel_requested" {
		return errInDoubt
	}
	got, err := r.operationHelpers(i)
	if err != nil {
		return err
	}
	if len(got) != len(expected) {
		return errInDoubt
	}
	// operationHelpers orders kind/key. Callers persist exactly this vector.
	for n := range got {
		if got[n] != expected[n] {
			return errInDoubt
		}
	}
	return nil
}
