package retention

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
	"time"
)

// Adoption is explicit operator evidence for an unregistered legacy log. It
// never claims command success or changes a bench's lease/checkpoint state.
type Adoption struct {
	Version, Root, ID string
	File              reap.Entry
	Size              int64
	ObservedAt        time.Time
	Digest            string
}

func (p Adoption) Hash() (string, error) {
	p.Digest = ""
	raw, e := json.Marshal(p)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func Inspect(db history.Reader, root, id string, protected map[string]bool) (*Adoption, error) {
	if protected[id] {
		return nil, errors.New("held execution log cannot be adopted")
	}
	if _, e := history.Get(db, id); e != sql.ErrNoRows {
		if e == nil {
			return nil, errors.New("log already has history")
		}
		return nil, e
	}
	f, file, e := lockFile(root, id)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return nil, e
	}
	p := &Adoption{Version: "superbench.log-adoption.v1", Root: root, ID: id, File: file, Size: st.Size(), ObservedAt: time.Now().UTC()}
	p.Digest, e = p.Hash()
	return p, e
}
func Adopt(tx *sql.Tx, root string, p Adoption, protected map[string]bool) (history.Record, error) {
	var zero history.Record
	hash, e := p.Hash()
	if e != nil {
		return zero, e
	}
	name, e := Name(p.ID)
	if e != nil {
		return zero, e
	}
	if p.Version != "superbench.log-adoption.v1" || p.Root != root || p.Digest != hash || p.File.Path != name || p.Size < 0 || p.ObservedAt.IsZero() || protected[p.ID] {
		return zero, errors.New("invalid or held log adoption")
	}
	if prior, e := history.Get(tx, p.ID); e == nil {
		if prior.Origin == "operator-adopted" && prior.AdoptionDigest == p.Digest {
			return prior, nil
		}
		return zero, errors.New("log already registered")
	} else if e != sql.ErrNoRows {
		return zero, e
	}
	f, file, e := lockFile(root, p.ID)
	if e != nil {
		return zero, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return zero, e
	}
	if file != p.File || st.Size() != p.Size {
		return zero, errors.New("legacy log changed after approval")
	}
	now := time.Now().UTC()
	r := history.Record{ID: p.ID, Origin: "operator-adopted", ObservedAt: p.ObservedAt, AdoptionDigest: p.Digest, Key: "unregistered", Holder: "operator", ReleasedAt: now, SealedAt: now, File: &file, Size: p.Size}
	if e = history.Start(tx, r); e != nil {
		return zero, e
	}
	return r, nil
}
