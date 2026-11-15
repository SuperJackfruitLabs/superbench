package service

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
	if state != "done" || barrier != "" || !status.Valid || status.Int64 < 100 || status.Int64 > 599 || len(body) > 64<<10 || !json.Valid(body) {
		return errInDoubt
	}
	return nil
}

// finalizeOperation runs after the original completed receipt commit, including
// restart replay of that cutoff. Its failure never reopens the original request
// or rewrites the historical response; retry only this metadata reconciliation.
func (s *Server) finalizeOperation(i operationcontrol.Intent) error {
	manager, err := s.config.Store.OperationCancellation()
	if err != nil {
		return err
	}
	return manager.Finish(i, func() error { return s.receipts.finalizationCutoff(i) })
}
