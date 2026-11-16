package bench

import (
	"context"
	"path/filepath"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/archivepolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
)

type ReapPlan = reap.Plan

func (s Store) archiveInspector() archivepolicy.Inspector {
	return archivepolicy.Inspector{Data: s.Data, Git: s.git, MirrorPath: s.mirrorPath, Clean: s.clean, Confirmed: s.confirmed, CheckpointError: ErrCheckpoint, ValidKey: keyPattern.MatchString}
}
func (s Store) Archive(key string) (*Bench, error) {
	return s.ArchiveWithGrace(key, archivepolicy.DefaultGrace)
}
func (s Store) ArchiveWithGrace(key string, grace time.Duration) (*Bench, error) {
	return s.ArchiveWithCheck(key, grace, nil)
}

// ArchiveWithCheck gates a new archive while holding its durable state lock.
// Retried archives preserve the original intent without rerunning check.
func (s Store) ArchiveWithCheck(key string, grace time.Duration, check func(*Bench) error) (*Bench, error) {
	return s.ArchiveContext(context.Background(), key, grace, func(_ context.Context, b *Bench) error {
		if check != nil {
			return check(b)
		}
		return nil
	})
}

func (s Store) ArchiveContext(ctx context.Context, key string, grace time.Duration, check func(context.Context, *Bench) error) (*Bench, error) {
	if err := s.RequireLegacy(key); err != nil {
		return nil, err
	}
	s, ctx, err := s.withControls(ctx, false)
	if err != nil {
		return nil, err
	}
	return (archivepolicy.Archiver{
		Update: func(key string, action func(*Bench) error) error {
			return s.transaction(func(db database) error {
				b, e := get(db, key)
				if e != nil {
					return e
				}
				return action(b)
			})
		},
		Inspector: s.archiveInspector(), Journal: s.freezeJournal,
		SaveIntent: s.saveLifecycleIntent, Busy: ErrBusy,
	}).Archive(key, grace, func(b *Bench) error {
		if check != nil {
			return check(ctx, b)
		}
		return nil
	})
}
func (s Store) reaper() archivepolicy.Reaper {
	return archivepolicy.Reaper{
		Update: func(key string, action func(*Bench) error) error {
			return s.transaction(func(db database) error {
				b, err := get(db, key)
				if err != nil {
					return err
				}
				return action(b)
			})
		},
		Read: func(key string, action func(*Bench) error) error {
			return s.read(func(db database) error {
				b, err := get(db, key)
				if err != nil {
					return err
				}
				return action(b)
			})
		},
		Inspector: s.archiveInspector(), Journal: s.reapJournal, FreezeJournal: s.freezeJournal,
		SaveIntent: s.saveLifecycleIntent, Git: s.git, ReleaseQuota: s.releaseQuota,
		AfterDelete: s.reapAfterDelete, Busy: ErrBusy, ValidKey: keyPattern.MatchString,
	}
}

func (s Store) PlanReap(key string) (*ReapPlan, error) { return s.reaper().Plan(key) }
func (s Store) reapJournal(key string) string          { return filepath.Join(s.State, "reap-"+key+".json") }

// Reap requires the concrete reviewed manifest. It journals approval before any
// deletion. Retry accepts only unchanged remaining entries, never new files.
func (s Store) Reap(p ReapPlan) (*Bench, error) {
	if err := reap.RequireNonRoot(); err != nil {
		return nil, err
	}
	if err := s.RequireLegacy(p.Key); err != nil {
		return nil, err
	}
	return s.reaper().Reap(p)
}
