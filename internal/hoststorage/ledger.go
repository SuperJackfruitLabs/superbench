package hoststorage

import (
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/allocationledger"
)

type policyBinding = allocationledger.Binding

func (p Policy) binding() policyBinding {
	return policyBinding{OperationJournal: p.OperationJournal, OwnerUID: p.OwnerUID, OwnerGID: p.OwnerGID, BenchBytes: p.BenchBytes, StoreBytes: p.StoreBytes, MirrorBytes: p.MirrorBytes, ReserveBytes: p.ReserveBytes, BenchInodes: p.BenchInodes, StoreInodes: p.StoreInodes, MirrorInodes: p.MirrorInodes, ReserveInodes: p.ReserveInodes}
}

func (p Policy) ledgerHeader(l ledger) error            { return l.Header(p.binding(), p.upgradingInodes) }
func (p Policy) bindLedger(l *ledger)                   { l.Bind(p.binding()) }
func (p Policy) request(kind string, size uint64) error { return p.binding().Request(kind, size) }
