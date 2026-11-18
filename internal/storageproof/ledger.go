package storageproof

import (
	"encoding/json"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/allocationledger"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storagecustody"
)

func OperationAuthorityRecords(root, ledgerPath string, binding allocationledger.Binding, upgrading bool, raw, anchor []byte) (string, string, error) {
	var l allocationledger.Ledger
	if err := StrictJSON(raw, &l); err != nil {
		return "", "", err
	}
	if !l.Bound() {
		return "", "", errors.New("operation authority requires existing bound v2 ledger")
	}
	if err := l.Validate(root, binding, upgrading); err != nil {
		return "", "", err
	}
	var owner storagecustody.OwnerRecord
	if err := StrictJSON(anchor, &owner); err != nil {
		return "", "", err
	}
	if owner != (storagecustody.OwnerRecord{Root: root, Ledger: ledgerPath}) {
		return "", "", errors.New("operation owner anchor differs from policy")
	}
	header := struct {
		FormatVersion        int
		Root, FilesystemUUID string
		Binding              *allocationledger.Binding
	}{2, l.Root, l.FilesystemUUID, l.Binding}
	canonical, _ := json.Marshal(header)
	return l.FilesystemUUID, Digest(canonical), nil
}

// LegacyReleaseLedger selects an already-known ordinary allocation before any
// quota effects. The caller must retain the existing allocation lock while reading.
func LegacyReleaseLedger(root string, binding allocationledger.Binding, upgrading bool, key string, project uint32, raw []byte) (bool, error) {
	var l allocationledger.Ledger
	if err := StrictJSON(raw, &l); err != nil {
		return false, err
	}
	if err := l.Validate(root, binding, upgrading); err != nil {
		return false, err
	}
	if l.FreshBench(key) {
		return true, nil
	}
	a, ok := l.Assignments["bench:"+key]
	if !ok || a.ProjectID != project {
		return false, errors.New("bench allocation does not match requested project")
	}
	return false, nil
}
