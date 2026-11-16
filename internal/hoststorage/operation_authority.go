package hoststorage

import (
	"encoding/json"
	"errors"
)

func (p Policy) operationAuthorityRecords(raw, anchor []byte) (string, string, error) {
	var l ledger
	if err := strictStorageJSON(raw, &l); err != nil {
		return "", "", err
	}
	if l.FormatVersion != 2 && l.FormatVersion != 3 {
		return "", "", errors.New("operation authority requires existing bound v2 ledger")
	}
	if err := p.checkLedger(l); err != nil {
		return "", "", err
	}
	var owner ownerRecord
	if err := strictStorageJSON(anchor, &owner); err != nil {
		return "", "", err
	}
	if owner != (ownerRecord{Root: p.Root, Ledger: p.Ledger}) {
		return "", "", errors.New("operation owner anchor differs from policy")
	}
	header := struct {
		FormatVersion        int
		Root, FilesystemUUID string
		Binding              *policyBinding
	}{2, l.Root, l.FilesystemUUID, l.Binding}
	canonical, _ := json.Marshal(header)
	return l.FilesystemUUID, storageDigest(canonical), nil
}
