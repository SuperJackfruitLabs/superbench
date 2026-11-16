package quotaoperationjournal

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// FreshLedger is callback-lifetime observation of one exclusively created fixed
// ledger. It carries no operation identity, path, quota limit or launch grant.
type FreshLedger struct{ state *freshLedgerState }
type freshLedgerState struct {
	mu      sync.Mutex
	active  bool
	observe func() (uint64, uint64, error)
}

func (f FreshLedger) Identity() (device, inode uint64, err error) {
	if f.state == nil {
		return 0, 0, ErrAuthority
	}
	f.state.mu.Lock()
	defer f.state.mu.Unlock()
	if !f.state.active || f.state.observe == nil {
		return 0, 0, ErrAuthority
	}
	return f.state.observe()
}
func (f FreshLedger) expire() {
	f.state.mu.Lock()
	defer f.state.mu.Unlock()
	f.state.active = false
}

type freshSession struct {
	config         Config
	owner          uint32
	file           *os.File
	anchor, ledger os.FileInfo
	expected       []byte
	authority      string
	parentCheck    func() error
}

func (s *freshSession) check(ctx context.Context) error {
	if err := s.parentCheck(); err != nil {
		return err
	}
	now, err := directory(s.config.Directory, s.owner)
	if err != nil || !os.SameFile(s.ledger, now) {
		return ErrAuthority
	}
	if err = checkAnchor(s.config.Directory, s.owner, s.anchor, s.expected); err != nil {
		return err
	}
	if s.authority != "" {
		actual, err := s.config.CheckReady(ctx)
		if err != nil {
			return err
		}
		if actual != s.authority || !validDigest(actual) {
			return ErrAuthority
		}
	}
	return ctx.Err()
}
func (s *freshSession) initialize(ctx context.Context, bootstrap func(context.Context, FreshLedger) error) (*Control, error) {
	backend := journalStore{Directory: s.config.Directory, owner: s.owner, Check: func() error { return s.check(ctx) }}
	backend.prepareFresh = func() error {
		capability := FreshLedger{state: &freshLedgerState{active: true, observe: func() (uint64, uint64, error) {
			if err := s.check(ctx); err != nil {
				return 0, 0, err
			}
			st := s.ledger.Sys().(*syscall.Stat_t)
			return uint64(st.Dev), uint64(st.Ino), nil
		}}}
		// Expire before publishing any readiness, including on callback failure.
		err := func() error { defer capability.expire(); return bootstrap(ctx, capability) }()
		if err != nil {
			return err
		}
		if err = s.check(ctx); err != nil {
			return err
		}
		actual, err := s.config.CheckReady(ctx)
		if err != nil {
			return err
		}
		if !validDigest(actual) {
			return ErrAuthority
		}
		s.authority = actual
		return s.check(ctx)
	}
	backend.finishFresh = func() error { return s.finish(ctx) }
	if err := backend.create(ctx, func(*Journal) error { return nil }); err != nil {
		return nil, err
	}
	return openExisting(ctx, s.config, s.owner)
}
func (s *freshSession) finish(ctx context.Context) error {
	if !validDigest(s.authority) {
		return ErrAuthority
	}
	if err := s.check(ctx); err != nil {
		return err
	}
	li, err := os.Lstat(filepath.Join(s.config.Directory, "operations.lock"))
	if err != nil {
		return err
	}
	di, err := os.Lstat(filepath.Join(s.config.Directory, "operations", "indexed-v2", "journal.db"))
	if err != nil {
		return err
	}
	if !private(li, s.owner, false) || !private(di, s.owner, false) {
		return ErrAuthority
	}
	ledger := s.ledger.Sys().(*syscall.Stat_t)
	lock := li.Sys().(*syscall.Stat_t)
	db := di.Sys().(*syscall.Stat_t)
	ready, err := json.Marshal(formatReady{uint64(ledger.Dev), uint64(ledger.Ino), uint64(lock.Dev), uint64(lock.Ino), uint64(db.Dev), uint64(db.Ino), s.authority})
	if err != nil {
		return err
	}
	ready = append(ready, '\n')
	if _, err = s.file.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	if _, err = s.file.Write(ready); err != nil {
		return err
	}
	if err = s.file.Sync(); err != nil {
		return err
	}
	s.expected = append(append([]byte(nil), s.expected...), ready...)
	return s.check(ctx)
}
