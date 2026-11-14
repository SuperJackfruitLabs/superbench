// Package retention collects only approved, released, sealed run output.
package retention

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
	"os"
	"path/filepath"
	"time"
)

const MaxItems = 1000
const MaxJournalBytes = 16 << 20

type Item struct {
	Record       history.Record
	RecordDigest string
	File         reap.Entry
}
type Prune struct {
	Record       history.Record
	RecordDigest string
}
type Collection struct {
	Digest, Status, Created, Finished string
	Count                             int
	RecordsPruned                     int
	CollectionsPruned                 int
	Bytes                             uint64
}
type Plan struct {
	Version        string
	Root           string
	CreatedAt      time.Time
	LogCutoff      time.Time
	MetadataCutoff time.Time
	Logs           []Item
	Records        []Prune
	Collections    []Collection
	Protected      []string
	Digest         string
}

func (p Plan) Hash() (string, error) {
	p.Digest = ""
	raw, e := json.Marshal(p)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func (p Plan) Validate(root string) error {
	hash, e := p.Hash()
	if e != nil {
		return e
	}
	raw, e := json.Marshal(p)
	if e != nil {
		return e
	}
	if p.CreatedAt.IsZero() || p.LogCutoff.After(p.CreatedAt) || p.MetadataCutoff.After(p.CreatedAt) || p.Version != "superbench.logs.v1" || p.Root != root || !filepath.IsAbs(root) || p.Digest != hash || len(raw) > reap.MaxManifest || len(p.Logs)+len(p.Records)+len(p.Collections) > MaxItems {
		return errors.New("invalid approved log collection plan")
	}
	seen := map[string]bool{}
	for _, item := range p.Logs {
		if !history.ID.MatchString(item.Record.ID) || seen[item.Record.ID] {
			return errors.New("duplicate/invalid collection run id")
		}
		seen[item.Record.ID] = true
		name, _ := Name(item.Record.ID)
		if item.File.Path != name || !os.FileMode(item.File.Mode).IsRegular() {
			return errors.New("invalid collection file")
		}
	}
	for _, item := range p.Records {
		if !history.ID.MatchString(item.Record.ID) || seen[item.Record.ID] {
			return errors.New("duplicate/invalid history prune id")
		}
		seen[item.Record.ID] = true
	}
	return nil
}
func ready(r history.Record, protected map[string]bool, cutoff time.Time) bool {
	return !protected[r.ID] && !r.ReleasedAt.IsZero() && r.File != nil && !r.SealedAt.IsZero() && !r.ReleasedAt.After(cutoff) && !r.SealedAt.After(cutoff) && r.CollectedAt.IsZero() && r.Collection == ""
}
func Build(db history.Reader, root string, protected map[string]bool, logAge, metadataAge time.Duration, limit int) (*Plan, error) {
	if logAge < 0 || metadataAge < 0 || limit < 1 || limit > MaxItems {
		return nil, errors.New("invalid retention age/limit")
	}
	if e := Directory(root); e != nil {
		return nil, e
	}
	now := time.Now().UTC()
	p := &Plan{Version: "superbench.logs.v1", Root: root, CreatedAt: now, LogCutoff: now.Add(-logAge), MetadataCutoff: now.Add(-metadataAge)}
	records, e := history.List(db, "", history.MaxRecords)
	if e != nil {
		return nil, e
	}
	// Oldest eligible records first, bounded by the requested operation size.
	for i := len(records) - 1; i >= 0; i-- {
		r := records[i]
		if len(p.Logs)+len(p.Records) >= limit {
			break
		}
		digest, e := history.Digest(r)
		if e != nil {
			return nil, e
		}
		if ready(r, protected, p.LogCutoff) {
			f, snapshot, e := lockFile(root, r.ID)
			if e != nil {
				p.Protected = append(p.Protected, r.ID)
				continue
			}
			f.Close()
			if snapshot != *r.File {
				p.Protected = append(p.Protected, r.ID)
				continue
			}
			p.Logs = append(p.Logs, Item{r, digest, snapshot})
		} else if !protected[r.ID] && !r.CollectedAt.IsZero() && !r.CollectedAt.After(p.MetadataCutoff) {
			p.Records = append(p.Records, Prune{r, digest})
		}
	}
	if db != nil {
		rows, e := db.Query("SELECT digest,status,created,finished,count,bytes,records_count,collections_count FROM log_collections WHERE status IN ('completed','canceled') ORDER BY finished LIMIT 10000")
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var c Collection
			if e = rows.Scan(&c.Digest, &c.Status, &c.Created, &c.Finished, &c.Count, &c.Bytes, &c.RecordsPruned, &c.CollectionsPruned); e != nil {
				rows.Close()
				return nil, e
			}
			finished, e := time.Parse(time.RFC3339Nano, c.Finished)
			if e != nil {
				rows.Close()
				return nil, e
			}
			if !finished.After(p.MetadataCutoff) && len(p.Logs)+len(p.Records)+len(p.Collections) < limit {
				p.Collections = append(p.Collections, c)
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
	}
	p.Digest, e = p.Hash()
	return p, e
}
func GetCollection(db history.Reader, digest string) (Collection, *Plan, error) {
	var c Collection
	var raw sql.NullString
	var finished sql.NullString
	if db == nil {
		return c, nil, sql.ErrNoRows
	}
	e := db.QueryRow("SELECT digest,status,created,finished,count,bytes,records_count,collections_count,plan FROM log_collections WHERE digest=?", digest).Scan(&c.Digest, &c.Status, &c.Created, &finished, &c.Count, &c.Bytes, &c.RecordsPruned, &c.CollectionsPruned, &raw)
	if e != nil {
		return c, nil, e
	}
	c.Finished = finished.String
	if !raw.Valid {
		return c, nil, nil
	}
	var p Plan
	if e = json.Unmarshal([]byte(raw.String), &p); e != nil {
		return c, nil, e
	}
	return c, &p, nil
}
