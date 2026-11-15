package service

import (
	"context"
	"sync"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationreceipts"
)

// Still private and not started by Serve: the proposed public route waits for
// actual managed restart acceptance. The caller must join this loop before
// closing the ledger; a privileged helper wait is never killed to speed shutdown.
func (s *Server) runOperationReconciler(ctx context.Context, wake <-chan struct{}, report func(error)) {
	runReconciliation(ctx, s.receipts, wake, func(ctx context.Context, w operationreceipts.ReconciliationWork) error {
		if w.Completed {
			return s.finalizeOperation(w.Intent)
		}
		_, err := s.reconcileCancellation(ctx, w.Intent.Target)
		return err
	}, report)
}

// Four fixed workers, four queued items, and no goroutine per cancel admission.
// Work admission never holds the receipt mutex while waiting for a helper/core
// lock. Failed items remain durable and are retried on a later inventory pass.
func runReconciliation(ctx context.Context, r *receipts, wake <-chan struct{}, execute func(context.Context, operationreceipts.ReconciliationWork) error, report func(error)) {
	jobs := make(chan operationreceipts.ReconciliationWork, 4)
	var workers sync.WaitGroup
	var mu sync.Mutex
	active := map[string]bool{}
	for n := 0; n < 4; n++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case w := <-jobs:
					var err error
					if ctx.Err() == nil {
						err = execute(ctx, w)
					}
					mu.Lock()
					delete(active, w.Intent.Target.ID())
					mu.Unlock()
					if err != nil && report != nil {
						report(err)
					}
				}
			}
		}()
	}
	defer workers.Wait()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	cursor := operationreceipts.WorkCursor{}
	scan := func() {
		for inspected := 0; inspected <= 10000; inspected += 64 {
			if ctx.Err() != nil {
				return
			}
			work, next, err := r.ReconciliationBatch(cursor)
			if err != nil {
				if report != nil {
					report(err)
				}
				return
			}
			cursor = next
			full := false
			for _, w := range work {
				id := w.Intent.Target.ID()
				mu.Lock()
				if active[id] {
					mu.Unlock()
					continue
				}
				select {
				case jobs <- w:
					active[id] = true
				default:
					full = true
				}
				mu.Unlock()
				if full {
					break
				}
			}
			if full || cursor == (operationreceipts.WorkCursor{}) {
				return
			}
		}
	}
	scan()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-wake:
			if !ok {
				wake = nil
			} else {
				scan()
			}
		case <-ticker.C:
			scan()
		}
	}
}
