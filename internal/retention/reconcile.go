package retention

import (
	"database/sql"
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
)

// Reconcile seals only a released, unprotected capture while holding its writer
// lock. An unknown exit remains unknown; reconciliation never invents success.
func Reconcile(tx *sql.Tx, root, id string, protected map[string]bool) (*history.Record, error) {
	r, e := history.Get(tx, id)
	if e != nil {
		return nil, e
	}
	if r.ReleasedAt.IsZero() || protected[id] {
		return nil, errors.New("release/reconcile the execution before sealing its log")
	}
	f, entry, e := lockFile(root, id)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if e = history.Seal(tx, id, entry, info.Size(), nil, false); e != nil {
		return nil, e
	}
	r, e = history.Get(tx, id)
	return &r, e
}
