package service

import "forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationreceipts"

type receipts = operationreceipts.Ledger
type receipt = operationreceipts.Receipt

var (
	errConflict        = operationreceipts.ErrConflict
	errInDoubt         = operationreceipts.ErrInDoubt
	errInProgress      = operationreceipts.ErrInProgress
	errCancelRequested = operationreceipts.ErrCancelRequested
	errCapacity        = operationreceipts.ErrCapacity
)

func openReceipts(state string) (*receipts, error) { return operationreceipts.Open(state) }
