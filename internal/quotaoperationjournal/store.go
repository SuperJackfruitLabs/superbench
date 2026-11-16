package quotaoperationjournal

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"

	_ "modernc.org/sqlite"
)

// Store's Directory is the fixed privileged ledger, never a request path.
// Production ownership is root. Check verifies the policy/root/binary binding.
type Store struct {
	Directory string
	// Identity pins this lock callback to exactly one original typed invocation.
	// Journal methods cannot substitute another operation, epoch or authority.
	Identity          quotaoperation.Identity
	Check             func(quotaoperation.Identity) error
	owner             uint32
	finishFresh       func() error
	anchored          bool
	anchoredCheck     func(quotaoperation.Identity) error
	anchoredDirectory string
}

// create refuses any legacy evidence or interrupted indexed creation. Migration
// requires a separate validator and is intentionally not an implicit fallback.
func (s Store) create(ctx context.Context, fn func(*Journal) error) error {
	return s.within(ctx, true, fn)
}
func (s Store) withinExisting(ctx context.Context, fn func(*Journal) error) error {
	return s.within(ctx, false, fn)
}

func (s Store) within(ctx context.Context, create bool, fn func(*Journal) error) error {
	if s.Check == nil || fn == nil || !s.Identity.Valid() || uint32(os.Geteuid()) != s.owner {
		return ErrAuthority
	}
	if err := s.Check(s.Identity); err != nil {
		return err
	}
	root, err := directory(s.Directory, s.owner)
	if err != nil {
		return err
	}
	checkAncestry, err := ancestry(s.Directory, s.owner)
	if err != nil {
		return err
	}
	lockPath := filepath.Join(s.Directory, "operations.lock")
	lf, err := openFile(lockPath, s.owner, create)
	// Only explicit fresh initialization may encounter an existing lock while
	// confirming an otherwise empty namespace. Normal operation never recreates
	// a missing lock, even when a prior process retains its old descriptor.
	if create && errors.Is(err, os.ErrExist) {
		lf, err = openFile(lockPath, s.owner, false)
	}
	if err != nil {
		return err
	}
	defer lf.Close()
	li, err := lf.Stat()
	if err != nil {
		return err
	}
	if err = lock(ctx, lf); err != nil {
		return err
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
	pins := []pinned{{s.Directory, root, true}, {lockPath, li, false}}
	var namespace func() error
	check := func() error {
		if e := checkAncestry(); e != nil {
			return e
		}
		for _, p := range pins {
			now, e := os.Lstat(p.path)
			if e != nil || !private(now, s.owner, p.directory) || !os.SameFile(p.info, now) {
				return ErrAuthority
			}
		}
		physical, e := filepath.EvalSymlinks(s.Directory)
		if e != nil || physical != s.Directory {
			return ErrAuthority
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		if namespace != nil {
			if e = namespace(); e != nil {
				return e
			}
		}
		return s.Check(s.Identity)
	}
	if err = check(); err != nil {
		return err
	}
	ops := filepath.Join(s.Directory, "operations")
	if create {
		if err = os.Mkdir(ops, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if err = syncDirectory(s.Directory); err != nil {
			return err
		}
	}
	oi, err := directory(ops, s.owner)
	if err != nil {
		return err
	}
	pins = append(pins, pinned{ops, oi, true})
	limit := 1
	if create {
		limit = 0
	}
	entries, err := boundedEntries(ops, limit)
	if err != nil {
		return err
	}
	if create && len(entries) != 0 {
		return ErrAuthority
	}
	if !create && (len(entries) != 1 || entries[0].Name() != "indexed-v2") {
		return ErrAuthority
	}
	indexed := filepath.Join(ops, "indexed-v2")
	if create {
		if err = os.Mkdir(indexed, 0700); err != nil {
			return err
		}
		if err = syncDirectory(ops); err != nil {
			return err
		}
	}
	ii, err := directory(indexed, s.owner)
	if err != nil {
		return err
	}
	pins = append(pins, pinned{indexed, ii, true})
	// The directory above durably blocks old helpers before any DB write. A
	// failure leaves it in place; future Create never treats it as an empty DB.
	path := filepath.Join(indexed, "journal.db")
	for _, name := range []string{path, path + "-journal"} {
		f, e := openFile(name, s.owner, create)
		if e != nil {
			return e
		}
		info, e := f.Stat()
		f.Close()
		if e != nil {
			return e
		}
		pins = append(pins, pinned{name, info, false})
	}
	if err = check(); err != nil {
		return err
	}
	namespace = func() error {
		for path, want := range map[string]map[string]bool{ops: {"indexed-v2": true}, indexed: {"journal.db": true, "journal.db-journal": true}} {
			entries, e := boundedEntries(path, len(want))
			if e != nil {
				return e
			}
			if len(entries) != len(want) {
				return ErrAuthority
			}
			for _, entry := range entries {
				if !want[entry.Name()] {
					return ErrAuthority
				}
			}
		}
		return nil
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("mode", "rw")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "synchronous(FULL)")
	q.Add("_pragma", "journal_mode(PERSIST)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err = db.PingContext(ctx); err != nil {
		return err
	}
	if err = check(); err != nil {
		return err
	}
	if create {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, schema); e != nil {
			return e
		}
		if e = check(); e != nil {
			return e
		}
		if e = tx.Commit(); e != nil {
			return e
		}
		if e = syncDirectory(indexed); e != nil {
			return e
		}
	}
	if err = validateSchema(db); err != nil {
		return err
	}
	// Unknown SQLite sidecars and foreign metadata are never silently ignored.
	files, err := boundedEntries(indexed, 2)
	if err != nil {
		return err
	}
	if len(files) != 2 {
		return ErrAuthority
	}
	for _, f := range files {
		if f.Name() != "journal.db" && f.Name() != "journal.db-journal" {
			return ErrAuthority
		}
	}
	j := &Journal{db: db, check: check, active: true, ctx: ctx, identity: s.Identity}
	defer func() { j.active = false }()
	if err = j.ready(); err != nil {
		return err
	}
	if err = fn(j); err != nil {
		return err
	}
	if err = validateSchema(db); err != nil {
		return err
	}
	if err = j.ready(); err != nil {
		return err
	}
	if create && s.finishFresh != nil {
		if err = s.finishFresh(); err != nil {
			return err
		}
	}
	return j.ready()
}
