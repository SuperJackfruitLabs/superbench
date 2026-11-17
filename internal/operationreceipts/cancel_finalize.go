package operationreceipts

import (
	"database/sql"
	"encoding/json"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

// finalizationCutoff recognizes the durable completed original response. It
// never substitutes response arrival, an expired timeout, or controller exit.
func (r *receipts) finalizationCutoff(i operationcontrol.Intent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.finalizationCutoffLocked(i)
}

func (r *receipts) finalizationCutoffLocked(i operationcontrol.Intent) error {
	stored, err := r.operationIntent(i.Target)
	if err != nil {
		return err
	}
	if stored != i {
		return errConflict
	}
	var digest, state, barrier string
	var body []byte
	var status sql.NullInt64
	if err = r.db.QueryRow(`SELECT digest,state,cancel_state,body,status FROM receipts WHERE owner=? AND id=?`, i.Target.UID, i.Target.RequestID).Scan(&digest, &state, &barrier, &body, &status); err != nil {
		return err
	}
	if digest != i.Target.Digest {
		return errConflict
	}
	limit, err := r.responseLimit(r.db, i.Target.UID, i.Target.RequestID)
	if err != nil {
		return errInDoubt
	}
	if state != "done" || barrier != "" || !status.Valid || status.Int64 < 100 || status.Int64 > 599 || int64(len(body)) > limit || !json.Valid(body) {
		return errInDoubt
	}
	return nil
}
