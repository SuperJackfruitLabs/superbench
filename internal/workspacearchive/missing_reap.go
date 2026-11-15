package workspacearchive

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"reflect"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/archivepolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

func (m Manager) reapMissing(ctx context.Context, p Plan, raw []byte) (*model.Bench, error) {
	if p.Missing == nil || p.Transfer == nil || p.Transfer.Recovery == nil {
		return nil, errors.New("missing reap lacks retirement evidence")
	}
	var committed *model.Bench
	err := m.Within(func(update workspace.Update) error {
		var observed *model.Bench
		if e := update(func(rows benchstate.Database, tx *sql.Tx) error {
			if e := ctx.Err(); e != nil {
				return e
			}
			b := rows[p.Key]
			if e := m.available(b); e != nil {
				return e
			}
			committed = workspace.Clone(b)
			if !missingSource(b) || !matches(&p, b) || b.Workspace.Archive.FrozenAt == "" {
				return errors.New("missing reap approval conflicts with source")
			}
			if e := m.verifyMissingArchive(ctx, tx, b); e != nil {
				return e
			}
			a := b.Workspace.Archive
			if a.ReapingDigest != "" {
				saved, e := readPlanFile(m.reapJournal(b.Key))
				if e != nil {
					return e
				}
				if a.ReapingDigest != p.Digest || !reflect.DeepEqual(saved, &p) {
					return errors.New("missing reap journal changed")
				}
				observed = workspace.Clone(b)
				return nil
			}
			if e := archivepolicy.Eligible(a.ArchivedAt, a.ReapAfter, time.Now()); e != nil {
				return e
			}
			current, e := m.planMissing(ctx, tx, b)
			if e != nil {
				return e
			}
			if current.Digest != p.Digest {
				return errors.New("missing source changed after reap review")
			}
			if e = m.SaveIntent(m.reapJournal(b.Key), raw); e != nil {
				return e
			}
			if e = ctx.Err(); e != nil {
				return e
			}
			b.Workspace.Phase = "reaping"
			a.ReapingDigest = p.Digest
			observed = workspace.Clone(b)
			return nil
		}); e != nil {
			return e
		}
		committed = observed
		if committed.Workspace.Phase == "reaped" {
			return nil
		}
		var finished *model.Bench
		if e := update(func(rows benchstate.Database, tx *sql.Tx) error {
			if e := ctx.Err(); e != nil {
				return e
			}
			b := rows[p.Key]
			if !reflect.DeepEqual(b, committed) {
				return errors.New("missing reap ownership changed")
			}
			if e := m.verifyMissingArchive(ctx, tx, b); e != nil {
				return e
			}
			approval, e := m.missingApproval(tx, b)
			if e != nil {
				return e
			}
			expectedRetirement := m.retirement(approval, *p.Missing)
			if approval.Storage.Managed && !reflect.DeepEqual(p.Retirement, &expectedRetirement) || !approval.Storage.Managed && p.Retirement != nil {
				return errors.New("missing retirement approval changed")
			}
			if !p.Missing.Root.Absent {
				if _, e = os.Lstat(b.Path); e == nil {
					if e = reap.DeleteFrozenContents(b.Path, p.Tree); e != nil {
						return e
					}
				} else if !os.IsNotExist(e) {
					return e
				}
			}
			if m.AfterDelete != nil {
				if e = m.AfterDelete(); e != nil {
					return e
				}
			}
			if e = ctx.Err(); e != nil {
				return e
			}
			if e = checkMissingRoot(*p.Missing, true); e != nil {
				return e
			}
			if !p.Missing.Root.Absent {
				if e = emptyApprovedRoot(b.Path, p.Tree); e != nil {
					return e
				}
			}
			if e = m.removeMissingRoot(ctx, b, p); e != nil {
				return e
			}
			if approval.Storage.Managed {
				if e = m.retireQuota(ctx, *p.Retirement); e != nil {
					return e
				}
				if e = m.verifyRetired(ctx, approval.Storage, *p.Missing); e != nil {
					return e
				}
			} else {
				if e = m.verifyRetired(ctx, approval.Storage, *p.Missing); e != nil {
					return e
				}
			}
			if e = ctx.Err(); e != nil {
				return e
			}
			b.Workspace.Phase = "reaped"
			b.Workspace.Archive.ReapedAt = now()
			finished = workspace.Clone(b)
			return nil
		}); e != nil {
			return e
		}
		committed = finished
		return nil
	})
	return workspace.Clone(committed), err
}
