package hoststorage

import (
	"encoding/json"
	"errors"
)

// Validate every row, but pin only this source's authority. Unrelated new bench
// allocations must not invalidate an already approved missing-checkout plan.
func (p Policy) verifyStorageRecords(key string, raw, anchor []byte) (Assignment, string, error) {
	var l ledger
	if err := strictStorageJSON(raw, &l); err != nil {
		return Assignment{}, "", err
	}
	if l.FormatVersion != 2 {
		return Assignment{}, "", errors.New("read-only verification requires an existing bound v2 ledger")
	}
	if err := p.checkLedger(l); err != nil {
		return Assignment{}, "", err
	}
	a, ok := l.Assignments["bench:"+key]
	if !Key.MatchString(key) || !ok || a.Bytes == 0 || a.Inodes == 0 {
		return Assignment{}, "", errors.New("bench allocation missing or retired")
	}
	var owner ownerRecord
	if err := strictStorageJSON(anchor, &owner); err != nil {
		return Assignment{}, "", err
	}
	if owner != (ownerRecord{Root: p.Root, Ledger: p.Ledger}) {
		return Assignment{}, "", errors.New("storage owner anchor differs from policy")
	}
	selected := struct {
		FormatVersion        int
		Root, FilesystemUUID string
		Binding              *policyBinding
		Key                  string
		Assignment           Assignment
	}{l.FormatVersion, l.Root, l.FilesystemUUID, l.Binding, key, a}
	canonical, _ := json.Marshal(selected)
	return a, storageDigest(canonical), nil
}
