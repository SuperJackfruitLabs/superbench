package archivepolicy

import (
	"errors"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
)

// Archiver preserves archive intent before applying the read-only transition.
// Update must load and durably commit the bench under one exclusive transaction.
type Archiver struct {
	Update     func(string, func(*model.Bench) error) error
	Inspector  Inspector
	Journal    func(string) string
	SaveIntent func(string, []byte) error
	Busy       error
}

// Archive never deletes data. A check gates only a new archive under its first
// transaction; retries finish durable intent without reevaluating the trigger.
func (a Archiver) Archive(key string, grace time.Duration, check func(*model.Bench) error) (*model.Bench, error) {
	if e := Duration(grace); e != nil {
		return nil, e
	}
	var out *model.Bench
	e := a.Update(key, func(b *model.Bench) error {
		var e error
		if e = a.Inspector.Paths(b); e != nil {
			return e
		}
		if b.Lease != nil {
			return a.Busy
		}
		if b.ArchivedAt == "" && check != nil {
			if e = check(b); e != nil {
				return e
			}
		}
		if b.ArchiveFreezeDigest == "" && b.ReapingDigest == "" && b.ReapedAt == "" {
			if e = a.Inspector.Checkout(b); e != nil {
				return e
			}
			plan, e := reap.NewFreeze(b.Key, b.Path, b.Checkpoint)
			if e != nil {
				return e
			}
			raw, e := plan.Bytes()
			if e != nil {
				return e
			}
			if e = a.SaveIntent(a.Journal(b.Key), raw); e != nil {
				return e
			}
			b.ArchiveFreezeDigest = plan.Digest
		}
		if b.ArchivedAt == "" {
			b.ArchivedAt = time.Now().UTC().Format(time.RFC3339Nano)
			b.ReapAfter, e = Deadline(b.ArchivedAt, grace)
			if e != nil {
				return e
			}
		}
		copy := *b
		out = &copy
		return nil
	})
	if e != nil || out.ReapingDigest != "" || out.ReapedAt != "" {
		return out, e
	}
	e = a.Update(key, func(b *model.Bench) error {
		var e error
		if b.ArchiveFreezeDigest != out.ArchiveFreezeDigest || b.Lease != nil || b.ReapingDigest != "" {
			return errors.New("archive permission intent changed")
		}
		plan, e := reap.ReadFreeze(a.Journal(key), b.Key, b.Path, b.Checkpoint, b.ArchiveFreezeDigest)
		if e != nil {
			return e
		}
		if e = reap.Freeze(b.Path, plan.Tree); e != nil {
			return e
		}
		if e = a.Inspector.Checkout(b); e != nil {
			return e
		}
		if b.ArchiveFrozenAt == "" {
			b.ArchiveFrozenAt = time.Now().UTC().Format(time.RFC3339Nano)
		}
		copy := *b
		out = &copy
		return nil
	})
	return out, e
}
