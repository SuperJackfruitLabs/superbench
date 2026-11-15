package bench

import (
	"database/sql"
	"encoding/json"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/output"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/retention"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/runprotection"
	"syscall"
	"time"
)

func protectedRuns(db database) map[string]bool { return runprotection.Runs(db) }
func (s Store) sealRunLog(id string, c *output.Capture, exit *int, truncated bool) error {
	entry, e := retention.Snapshot(s.State, id)
	if e != nil {
		return e
	}
	info, e := c.File.Stat()
	if e != nil {
		return e
	}
	if entry.Device != uint64(info.Sys().(*syscall.Stat_t).Dev) || entry.Inode != info.Sys().(*syscall.Stat_t).Ino {
		return errors.New("captured log identity changed")
	}
	return s.transactionSQL(func(_ database, tx *sql.Tx) error { return history.Seal(tx, id, entry, info.Size(), exit, truncated) })
}
func (s Store) History(key string, limit int) ([]history.Record, error) {
	var out []history.Record
	e := s.readSQL(func(_ database, db reader) error { var e error; out, e = history.List(db, key, limit); return e })
	return out, e
}
func (s Store) ReconcileLog(id string) (*history.Record, error) {
	var out *history.Record
	e := s.transactionSQL(func(records database, tx *sql.Tx) error {
		r, e := history.Get(tx, id)
		if e != nil {
			return e
		}
		if r.ReleasedAt.IsZero() || protectedRuns(records)[id] {
			return errors.New("release/reconcile the execution before sealing its log")
		}
		f, entry, e := retention.LockSnapshot(s.State, id)
		if e != nil {
			return e
		}
		defer f.Close()
		info, e := f.Stat()
		if e != nil {
			return e
		}
		if e = history.Seal(tx, id, entry, info.Size(), nil, false); e != nil {
			return e
		}
		r, e = history.Get(tx, id)
		out = &r
		return e
	})
	return out, e
}
func (s Store) PlanCollection(logAge, metadataAge time.Duration, limit int) (*retention.Plan, error) {
	var out *retention.Plan
	e := s.readSQL(func(records database, db reader) error {
		var e error
		out, e = retention.Build(db, s.State, protectedRuns(records), logAge, metadataAge, limit)
		return e
	})
	return out, e
}
func (s Store) Collect(p retention.Plan) (retention.Collection, error) {
	var out retention.Collection
	e := s.transactionSQL(func(records database, tx *sql.Tx) error {
		raw, e := json.Marshal(p)
		if e != nil {
			return e
		}
		space, e := s.space(s.State)
		if e != nil {
			return e
		}
		if space.Free < uint64(len(raw))*2+(1<<20) {
			return errors.New("state reserve cannot journal this collection; use a smaller plan")
		}
		return retention.Prepare(tx, s.State, p, protectedRuns(records))
	})
	if e != nil {
		return out, e
	}
	e = s.transactionSQL(func(records database, tx *sql.Tx) error {
		var e error
		out, e = retention.Apply(tx, s.State, p, protectedRuns(records), s.collectAfterDelete)
		return e
	})
	return out, e
}
func (s Store) ResumeCollection(digest string, cancel bool) (retention.Collection, error) {
	var out retention.Collection
	var p *retention.Plan
	e := s.readSQL(func(_ database, db reader) error {
		var e error
		out, p, e = retention.GetCollection(db, digest)
		return e
	})
	if e != nil {
		return out, e
	}
	if cancel {
		e = s.transactionSQL(func(records database, tx *sql.Tx) error {
			var e error
			out, e = retention.Cancel(tx, s.State, digest, protectedRuns(records))
			return e
		})
		return out, e
	}
	if out.Status == "completed" {
		return out, nil
	}
	if p == nil {
		return out, errors.New("collection is canceled or has no retained plan")
	}
	return s.Collect(*p)
}
func (s Store) Collections() ([]retention.Collection, error) {
	out := []retention.Collection{}
	e := s.readSQL(func(_ database, db reader) error {
		if db == nil {
			return nil
		}
		rows, e := db.Query("SELECT digest,status,created,coalesce(finished,''),count,bytes,records_count,collections_count FROM log_collections ORDER BY created LIMIT 10000")
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var c retention.Collection
			if e = rows.Scan(&c.Digest, &c.Status, &c.Created, &c.Finished, &c.Count, &c.Bytes, &c.RecordsPruned, &c.CollectionsPruned); e != nil {
				return e
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, e
}

func (s Store) InspectLog(id string) (*retention.Adoption, error) {
	var out *retention.Adoption
	e := s.readSQL(func(records database, db reader) error {
		var e error
		out, e = retention.Inspect(db, s.State, id, protectedRuns(records))
		return e
	})
	return out, e
}
func (s Store) AdoptLog(p retention.Adoption) (history.Record, error) {
	var out history.Record
	e := s.transactionSQL(func(records database, tx *sql.Tx) error {
		var e error
		out, e = retention.Adopt(tx, s.State, p, protectedRuns(records))
		return e
	})
	return out, e
}
