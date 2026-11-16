package handoffjournal

import "database/sql"

// OfferByKey retains the fixed namespace reservation even after completion.
func OfferByKey(db Reader, key string) (Offer, error) {
	var id string
	if err := db.QueryRow(`SELECT creation_id FROM workspace_handoff_offers WHERE key=?`, key).Scan(&id); err != nil {
		return Offer{}, err
	}
	return OfferByID(db, id)
}

// Unreserved precedes every ordinary creation effect, under its host lock.
func Unreserved(db Reader, key string) error {
	_, err := OfferByKey(db, key)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	return ErrAuthority
}

// RejectHistorical prevents upgrading future tables into older authority.
func RejectHistorical(db Reader) error {
	var n int
	err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('workspace_handoff_identity','workspace_handoff_offers','workspace_handoff_exports','workspace_handoff_imports')`).Scan(&n)
	if err != nil {
		return err
	}
	if n != 0 {
		return ErrAuthority
	}
	return nil
}
