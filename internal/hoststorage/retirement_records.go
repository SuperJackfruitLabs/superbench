package hoststorage

import (
	"encoding/json"
	"errors"
)

// A retired row cannot reconstruct the old authority. Only the original pinned
// approval can supply it; compare its ID/path and revalidate the complete ledger.
func (p Policy) retirementRecords(key string, raw, anchor []byte, original Assignment) (Assignment, string, bool, error) {
	var l ledger
	if err := strictStorageJSON(raw, &l); err != nil {
		return Assignment{}, "", false, err
	}
	if l.FormatVersion != 2 && l.FormatVersion != 3 {
		return Assignment{}, "", false, errors.New("retirement requires a bound v2 ledger")
	}
	if err := p.checkLedger(l); err != nil {
		return Assignment{}, "", false, err
	}
	actual, ok := l.Assignments["bench:"+key]
	retired := ok && actual.Bytes == 0 && actual.Inodes == 0
	if !ok || actual.ProjectID != original.ProjectID || actual.Path != original.Path || (!retired && actual != original) {
		return Assignment{}, "", false, errors.New("retirement source assignment changed")
	}
	if retired {
		l.Assignments["bench:"+key] = original
	}
	selectedRaw, err := json.Marshal(l)
	if err != nil {
		return Assignment{}, "", false, err
	}
	a, digest, err := p.verifyStorageRecords(key, selectedRaw, anchor)
	return a, digest, retired, err
}
