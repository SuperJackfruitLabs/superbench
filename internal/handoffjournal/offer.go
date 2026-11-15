package handoffjournal

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/crosshost"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotapolicy"
)

const OfferVersion = "superbench.cross-host-offer.v1"
const maxOffer = 4096

type Offer struct {
	Version     string
	Destination crosshost.Destination
	QuotaBytes  uint64
	CreatedAt   time.Time
}

func (o Offer) valid() bool {
	_, offset := o.CreatedAt.Zone()
	return o.Version == OfferVersion && o.Destination.Valid() && quotapolicy.ValidBytes(o.QuotaBytes) && !o.CreatedAt.IsZero() && offset == 0
}

func sameOffer(a, b Offer) bool {
	return a.Version == b.Version && a.Destination == b.Destination && a.QuotaBytes == b.QuotaBytes && a.CreatedAt.Equal(b.CreatedAt)
}

func OfferByID(db Reader, creationID string) (Offer, error) {
	var o Offer
	var raw []byte
	var workspaceID, key string
	err := db.QueryRow(`SELECT workspace_id,key,CASE WHEN length(body)<=? THEN body ELSE NULL END FROM workspace_handoff_offers WHERE creation_id=?`, maxOffer, creationID).Scan(&workspaceID, &key, &raw)
	if err != nil {
		return o, err
	}
	if json.Unmarshal(raw, &o) != nil || !o.valid() || o.Destination.CreationID != creationID || o.Destination.WorkspaceID != workspaceID || o.Destination.Key != key {
		return Offer{}, ErrAuthority
	}
	canonical, err := json.Marshal(o)
	if err != nil || !bytes.Equal(canonical, raw) {
		return Offer{}, ErrAuthority
	}
	state, err := StateID(db)
	if err != nil || state != o.Destination.StateID {
		return Offer{}, ErrAuthority
	}
	return o, nil
}

// ReserveOffer runs with the core's host lock and transaction. Its caller must
// independently inspect the key/path absence and actual host before this write.
// No checkout, source selection, quota allocation or lease is created here.
func ReserveOffer(tx *sql.Tx, o Offer) error {
	state, err := StateID(tx)
	if err != nil || !o.valid() || state != o.Destination.StateID {
		return ErrAuthority
	}
	prior, err := OfferByID(tx, o.Destination.CreationID)
	if err == nil {
		if !sameOffer(prior, o) {
			return ErrAuthority
		}
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	if err := budget(tx, "workspace_handoff_offers"); err != nil {
		return err
	}
	raw, err := json.Marshal(o)
	if err != nil || len(raw) > maxOffer {
		return ErrAuthority
	}
	r, err := tx.Exec(`INSERT INTO workspace_handoff_offers(creation_id,workspace_id,key,body) VALUES(?,?,?,?)`, o.Destination.CreationID, o.Destination.WorkspaceID, o.Destination.Key, raw)
	if err != nil {
		return err
	}
	if n, err := r.RowsAffected(); err != nil || n != 1 {
		return ErrAuthority
	}
	got, err := OfferByID(tx, o.Destination.CreationID)
	if err != nil || !sameOffer(got, o) {
		return ErrAuthority
	}
	return nil
}
