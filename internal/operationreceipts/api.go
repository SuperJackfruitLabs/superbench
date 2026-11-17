package operationreceipts

import (
	"context"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"strconv"
)

// Ledger serializes receipt admission, effect gates and cancellation. Its DB and
// mutex never escape to the HTTP controller or the engine.
type Ledger = receipts
type Receipt = receipt

var (
	ErrConflict        = errConflict
	ErrInDoubt         = errInDoubt
	ErrInProgress      = errInProgress
	ErrCancelRequested = errCancelRequested
	ErrCapacity        = errCapacity
)

func Open(state string) (*Ledger, error) { return openReceipts(state) }
func (r *receipts) Close() error         { return r.db.Close() }
func (r *receipt) Bytes() []byte         { return r.body }
func (r *receipt) HTTPStatus() int       { return r.status }
func (r *receipts) Claim(uid uint32, id, digest string) (*Receipt, error) {
	return r.claim(uid, id, digest)
}

// ClaimLimit is Claim with an explicit whole-response bound. Only the legacy
// bound or a finite bound up to 2 MiB is accepted; a finite bound creates
// response_limits (format 7) on first use and is charged before insertion.
func (r *receipts) ClaimLimit(uid uint32, id, digest string, limit int) (*Receipt, error) {
	if !validResponseLimit(int64(limit)) {
		return nil, errInDoubt
	}
	return r.claimLimit(uid, id, digest, int64(limit))
}

// LegacyResponseLimit is the unprofiled retained-response bound.
const LegacyResponseLimit = int(legacyResponseLimit)

func (r *receipts) Lookup(uid uint32, id, digest string) (*Receipt, bool, error) {
	return r.lookup(uid, id, digest)
}
func (r *receipts) Finish(uid uint32, id string, body []byte, status int) error {
	return r.finish(uid, id, body, status)
}
func (r *receipts) Scope(ctx context.Context, i operationcontrol.Intent) (context.Context, context.CancelFunc, error) {
	return r.operationScope(ctx, i)
}
func (r *receipts) RequestCancel(uid uint32, id, digest string) (string, error) {
	return r.requestCancelV2(uid, id, digest)
}
func (r *receipts) FinalizationCutoff(i operationcontrol.Intent) error {
	return r.finalizationCutoff(i)
}
func (r *receipts) MarkCoreFinalized(i operationcontrol.Intent) error { return r.markCoreFinalized(i) }
func (r *receipts) CompletedOperations() ([]operationcontrol.Intent, error) {
	return r.completedOperations()
}
func (r *receipts) ConfirmCancellation(i operationcontrol.Intent) error {
	return r.confirmCancellation(i)
}
func (r *receipts) VerifyCancellationInventory(i operationcontrol.Intent, h []operationcontrol.Helper) error {
	return r.verifyCancellationInventory(i, h)
}
func receiptKey(uid uint32, id string) string { return strconv.FormatUint(uint64(uid), 10) + ":" + id }
