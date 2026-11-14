package retention

import (
	"database/sql"
	"encoding/json"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"os"
	"path/filepath"
	"time"
)

func sameRecord(r history.Record, digest string) bool {
	actual, e := history.Digest(r)
	return e == nil && actual == digest
}
func Prepare(tx *sql.Tx, root string, p Plan, protected map[string]bool) error {
	if e := p.Validate(root); e != nil {
		return e
	}
	if c, old, e := GetCollection(tx, p.Digest); e == nil {
		if c.Status == "canceled" {
			return errors.New("collection canceled; review a fresh plan")
		}
		if old != nil {
			return old.Validate(root)
		}
		return nil
	} else if e != sql.ErrNoRows {
		return e
	}
	if len(p.Logs)+len(p.Records)+len(p.Collections) == 0 {
		return errors.New("no retention candidates")
	}
	for _, item := range p.Logs {
		r, e := history.Get(tx, item.Record.ID)
		if e != nil {
			return e
		}
		if !sameRecord(r, item.RecordDigest) || !ready(r, protected, p.LogCutoff) || !sameRecord(item.Record, item.RecordDigest) {
			return errors.New("log collection record changed")
		}
		f, file, e := lockFile(root, r.ID)
		if e != nil {
			return e
		}
		f.Close()
		if file != item.File || r.File == nil || file != *r.File {
			return errors.New("log changed after approval")
		}
	}
	for _, item := range p.Records {
		r, e := history.Get(tx, item.Record.ID)
		if e != nil {
			return e
		}
		if protected[r.ID] || r.CollectedAt.IsZero() || r.CollectedAt.After(p.MetadataCutoff) || !sameRecord(r, item.RecordDigest) || !sameRecord(item.Record, item.RecordDigest) {
			return errors.New("history prune record changed")
		}
	}
	for _, item := range p.Collections {
		c, _, e := GetCollection(tx, item.Digest)
		if e != nil {
			return e
		}
		if c != item || (c.Status != "completed" && c.Status != "canceled") {
			return errors.New("collection prune record changed")
		}
	}
	raw, e := json.Marshal(p)
	if e != nil {
		return e
	}
	var count, bytes int
	if e = tx.QueryRow("SELECT count(*),coalesce(sum(length(plan)),0) FROM log_collections").Scan(&count, &bytes); e != nil {
		return e
	}
	if count >= 10000 || bytes+len(raw) > MaxJournalBytes {
		return errors.New("collection journal budget exhausted")
	}
	var size uint64
	for _, item := range p.Logs {
		size += uint64(item.Record.FileSize())
	}
	if _, e = tx.Exec("INSERT INTO log_collections(digest,plan,status,created,count,bytes,records_count,collections_count) VALUES(?,?,'prepared',?,?,?,?,?)", p.Digest, string(raw), p.CreatedAt.Format(time.RFC3339Nano), len(p.Logs), size, len(p.Records), len(p.Collections)); e != nil {
		return e
	}
	for _, item := range p.Logs {
		r, e := history.Get(tx, item.Record.ID)
		if e != nil {
			return e
		}
		r.Collection = p.Digest
		if e = history.Save(tx, r); e != nil {
			return e
		}
	}
	return nil
}
func Apply(tx *sql.Tx, root string, p Plan, protected map[string]bool, afterDelete func() error) (Collection, error) {
	var zero Collection
	if e := p.Validate(root); e != nil {
		return zero, e
	}
	c, saved, e := GetCollection(tx, p.Digest)
	if e != nil {
		return zero, e
	}
	if c.Status == "completed" {
		return c, nil
	}
	if c.Status != "prepared" || saved == nil {
		return zero, errors.New("collection is not prepared")
	}
	if e = saved.Validate(root); e != nil {
		return zero, e
	}
	var files []*os.File
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	var present []string
	for _, item := range p.Logs {
		r, e := history.Get(tx, item.Record.ID)
		if e != nil {
			return zero, e
		}
		if protected[r.ID] || r.Collection != p.Digest || !sameRecord(r, item.RecordDigest) {
			return zero, errors.New("collection authority changed")
		}
		f, snapshot, e := lockFile(root, r.ID)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return zero, e
		}
		files = append(files, f)
		if snapshot != item.File {
			return zero, errors.New("remaining log changed after approval")
		}
		present = append(present, filepath.Join(root, snapshot.Path))
	}
	for _, item := range p.Records {
		r, e := history.Get(tx, item.Record.ID)
		if e == sql.ErrNoRows {
			continue
		}
		if e != nil {
			return zero, e
		}
		if protected[r.ID] || r.CollectedAt.IsZero() || !sameRecord(r, item.RecordDigest) {
			return zero, errors.New("remaining history changed")
		}
	}
	for _, item := range p.Collections {
		old, _, e := GetCollection(tx, item.Digest)
		if e == sql.ErrNoRows {
			continue
		}
		if e != nil {
			return zero, e
		}
		if old != item {
			return zero, errors.New("remaining collection summary changed")
		}
	}
	for _, path := range present {
		if e = os.Remove(path); e != nil {
			return zero, e
		}
	}
	dir, e := os.Open(root)
	if e != nil {
		return zero, e
	}
	e = dir.Sync()
	dir.Close()
	if e != nil {
		return zero, e
	}
	if afterDelete != nil {
		if e = afterDelete(); e != nil {
			return zero, e
		}
	}
	now := time.Now().UTC()
	for _, item := range p.Logs {
		r, e := history.Get(tx, item.Record.ID)
		if e != nil {
			return zero, e
		}
		r.CollectedAt = now
		r.CollectedSHA256 = item.File.SHA256
		r.CollectionReason = "approved"
		if e = history.Save(tx, r); e != nil {
			return zero, e
		}
	}
	for _, item := range p.Records {
		if _, e = tx.Exec("DELETE FROM run_history WHERE id=?", item.Record.ID); e != nil {
			return zero, e
		}
	}
	for _, item := range p.Collections {
		if _, e = tx.Exec("DELETE FROM log_collections WHERE digest=?", item.Digest); e != nil {
			return zero, e
		}
	}
	c.Status = "completed"
	c.Finished = now.Format(time.RFC3339Nano)
	_, e = tx.Exec("UPDATE log_collections SET status='completed',finished=?,plan=NULL WHERE digest=?", c.Finished, p.Digest)
	return c, e
}
func Cancel(tx *sql.Tx, root, digest string, protected map[string]bool) (Collection, error) {
	c, p, e := GetCollection(tx, digest)
	if e != nil {
		return c, e
	}
	if c.Status == "canceled" {
		return c, nil
	}
	if c.Status != "prepared" || p == nil {
		return c, errors.New("only a prepared collection can be canceled")
	}
	if e = p.Validate(root); e != nil {
		return c, e
	}
	now := time.Now().UTC()
	for _, item := range p.Logs {
		r, e := history.Get(tx, item.Record.ID)
		if e != nil {
			return c, e
		}
		if protected[r.ID] || r.Collection != digest || !sameRecord(r, item.RecordDigest) {
			return c, errors.New("collection authority changed")
		}
		name, _ := Name(r.ID)
		_, e = os.Lstat(filepath.Join(root, name))
		if e != nil && !os.IsNotExist(e) {
			return c, e
		}
		if os.IsNotExist(e) {
			r.CollectedAt = now
			r.CollectedSHA256 = item.File.SHA256
			r.CollectionReason = "absent on cancellation"
		} else {
			r.Collection = ""
		}
		r.Revision++
		if e = history.Save(tx, r); e != nil {
			return c, e
		}
	}
	c.Status = "canceled"
	c.Finished = now.Format(time.RFC3339Nano)
	_, e = tx.Exec("UPDATE log_collections SET status='canceled',finished=?,plan=NULL WHERE digest=?", c.Finished, digest)
	return c, e
}
