package operationreceipts

import (
	"context"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

// operationScope is private until the proposed wire contract and all engine
// effect boundaries are verified. Its gate shares the durable receipt mutex;
// neither cancellation admission nor this gate acquires the host lock.
func (r *receipts) operationScope(parent context.Context, i operationcontrol.Intent) (context.Context, context.CancelFunc, error) {
	if _, exists := operationcontrol.FromContext(parent); exists {
		return nil, nil, errConflict
	}
	if !i.Valid() {
		return nil, nil, errConflict
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.operationScopeLocked(parent, i, false)
}

func (r *receipts) operationScopeLocked(parent context.Context, i operationcontrol.Intent, intentCommitted bool) (context.Context, context.CancelFunc, error) {
	if err := r.operationAdmission(i); err != nil {
		return nil, nil, err
	}
	key := receiptKey(i.Target.UID, i.Target.RequestID)
	if r.controllers == nil {
		r.controllers = map[string]func(){}
	}
	if _, ok := r.controllers[key]; ok {
		return nil, nil, errInDoubt
	}
	if intentCommitted {
		stored, err := r.operationIntent(i.Target)
		if err != nil {
			return nil, nil, err
		}
		if stored != i {
			return nil, nil, errConflict
		}
		if err = r.requireHelperTracking(i); err != nil {
			return nil, nil, err
		}
	} else if err := r.saveOperationIntent(i); err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	scoped, err := operationcontrol.WithScope(ctx, i, func(fn func() error) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		if err := r.operationAdmission(i); err != nil {
			return err
		}
		return fn()
	})
	if err != nil {
		cancel()
		return nil, nil, err
	}
	scoped, err = operationcontrol.WithHelperRecorder(scoped, func(h operationcontrol.Helper) error { return r.recordOperationHelper(i, h) })
	if err != nil {
		cancel()
		return nil, nil, err
	}
	r.controllers[key] = cancel
	return scoped, cancel, nil
}
func (r *receipts) operationAdmission(i operationcontrol.Intent) error {
	var digest, state, barrier string
	if err := r.db.QueryRow(`SELECT digest,state,cancel_state FROM receipts WHERE owner=? AND id=?`, i.Target.UID, i.Target.RequestID).Scan(&digest, &state, &barrier); err != nil {
		return errInDoubt
	}
	if digest != i.Target.Digest {
		return errConflict
	}
	if barrier != "" {
		return errCancelRequested
	}
	if state != "running" || !r.active[receiptKey(i.Target.UID, i.Target.RequestID)] {
		return errInDoubt
	}
	return nil
}
