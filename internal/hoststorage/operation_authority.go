package hoststorage

import "forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storageproof"

func (p Policy) operationAuthorityRecords(raw, anchor []byte) (string, string, error) {
	return storageproof.OperationAuthorityRecords(p.Root, p.Ledger, p.binding(), p.upgradingInodes, raw, anchor)
}
