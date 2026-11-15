package crosshostflow

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/handoffjournal"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hostidentity"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotapolicy"
)

func OfferBytes(o handoffjournal.Offer) ([]byte, error) {
	_, zone := o.CreatedAt.Zone()
	if zone != 0 || o.Version != handoffjournal.OfferVersion || !o.Destination.Valid() || !quotapolicy.ValidBytes(o.QuotaBytes) || o.CreatedAt.IsZero() {
		return nil, errors.New("invalid destination offer")
	}
	raw, err := json.Marshal(o)
	if len(raw) > 4096 {
		return nil, errors.New("destination offer exceeds bound")
	}
	return raw, err
}
func ParseOffer(raw []byte) (handoffjournal.Offer, error) {
	var o handoffjournal.Offer
	if len(raw) > 4096 || !utf8.Valid(raw) {
		return o, errors.New("invalid destination offer encoding")
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		return o, err
	}
	expected, err := OfferBytes(o)
	if err != nil || !bytes.Equal(bytes.TrimSpace(raw), expected) {
		return handoffjournal.Offer{}, errors.New("noncanonical destination offer")
	}
	return o, nil
}
func absent(path string) error {
	_, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("handoff destination has existing files; preserve and reconcile")
}

// Offer atomically reserves the normal core namespace without allocating data.
func (m Manager) Offer(ctx context.Context, keyName, creationID string, quota uint64) (*handoffjournal.Offer, error) {
	if !key.MatchString(keyName) || !key.MatchString(creationID) || !quotapolicy.ValidBytes(quota) {
		return nil, errors.New("invalid destination reservation")
	}
	ctx, cancel, a, err := m.admin(ctx)
	defer cancel()
	if err != nil {
		return nil, err
	}
	host, err := hostidentity.Current(ctx)
	if err != nil {
		return nil, err
	}
	var out *handoffjournal.Offer
	err = a.Prep.Update(func(rows benchstate.Database, tx *sql.Tx) error {
		state, err := handoffjournal.StateID(tx)
		if err != nil {
			return err
		}
		if prior, e := handoffjournal.OfferByID(tx, creationID); e == nil {
			if prior.Destination.HostID != host || prior.Destination.StateID != state || prior.Destination.Key != keyName || prior.QuotaBytes != quota {
				return handoffjournal.ErrAuthority
			}
			out = &prior
			return ctx.Err()
		} else if e != sql.ErrNoRows {
			return e
		}
		if rows[keyName] != nil {
			return errors.New("destination key already registered")
		}
		for _, p := range []string{filepath.Join(m.Store.Data, "benches", keyName), filepath.Join(m.Store.State, "create-"+keyName+".json")} {
			if err := absent(p); err != nil {
				return err
			}
		}
		id, err := model.Nonce()
		if err != nil {
			return err
		}
		o := handoffjournal.Offer{Version: handoffjournal.OfferVersion, QuotaBytes: quota, CreatedAt: time.Now().UTC()}
		o.Destination.HostID = host
		o.Destination.StateID = state
		o.Destination.WorkspaceID = id
		o.Destination.Key = keyName
		o.Destination.CreationID = creationID
		if err = handoffjournal.ReserveOffer(tx, o); err != nil {
			return err
		}
		out = &o
		return ctx.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
