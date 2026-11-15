package repaircontrol

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
)

const databaseName = "repair-operations.db"
const lockName = "repair-operations.lock"

type Journal struct {
	db                                *sql.DB
	lock                              *os.File
	state, instance                   string
	owner                             uint32
	directory, database, lockIdentity os.FileInfo
	mu                                sync.Mutex
	closed                            bool
}

// Create is only for Claim. An interrupted/foreign existing database is never
// initialized as a fresh incarnation. Inspect and Continue use OpenExisting.
func Create(state string) (*Journal, error) { return open(state, "", true) }
func OpenExisting(state, instance string) (*Journal, error) {
	if !id32.MatchString(instance) {
		return nil, ErrAuthority
	}
	return open(state, instance, false)
}
func (j *Journal) Instance() string { return j.instance }

func open(state, instance string, create bool) (j *Journal, err error) {
	physical, err := filepath.EvalSymlinks(state)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(state) || filepath.Clean(state) != state || physical != state || state == "/" {
		return nil, ErrAuthority
	}
	directory, err := os.Lstat(state)
	if err != nil {
		return nil, err
	}
	owner := directory.Sys().(*syscall.Stat_t).Uid
	if !directory.IsDir() || directory.Mode().Perm()&0077 != 0 || os.Geteuid() != 0 && owner != uint32(os.Geteuid()) {
		return nil, ErrAuthority
	}
	j = &Journal{state: state, directory: directory, owner: owner}
	owned := j
	defer func() {
		if err != nil {
			if owned.db != nil {
				owned.db.Close()
			}
			if owned.lock != nil {
				owned.lock.Close()
			}
			j = nil
		}
	}()
	lockFresh := false
	if create {
		j.lock, err = os.OpenFile(filepath.Join(state, lockName), os.O_RDWR|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
		lockFresh = err == nil
		if errors.Is(err, os.ErrExist) {
			j.lock, err = os.OpenFile(filepath.Join(state, lockName), os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		}
	} else {
		j.lock, err = os.OpenFile(filepath.Join(state, lockName), os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	}
	if err != nil {
		return nil, err
	}
	if lockFresh && os.Geteuid() == 0 {
		if err = j.lock.Chown(int(owner), int(directory.Sys().(*syscall.Stat_t).Gid)); err != nil {
			return nil, err
		}
	}
	j.lockIdentity, err = privateRegular(j.lock, owner)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = j.flock(ctx); err != nil {
		return nil, err
	}
	defer syscall.Flock(int(j.lock.Fd()), syscall.LOCK_UN)
	path := filepath.Join(state, databaseName)
	fresh := false
	var f *os.File
	if create {
		f, err = os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
		fresh = err == nil
		if errors.Is(err, os.ErrExist) {
			f, err = os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		}
	} else {
		f, err = os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	}
	if err != nil {
		return nil, err
	}
	if fresh && os.Geteuid() == 0 {
		if err = f.Chown(int(owner), int(directory.Sys().(*syscall.Stat_t).Gid)); err != nil {
			f.Close()
			return nil, err
		}
	}
	j.database, err = privateRegular(f, owner)
	f.Close()
	if err != nil {
		return nil, err
	}
	j.db, err = statestore.Open(path, false)
	if err != nil {
		return nil, err
	}
	if fresh {
		var nonce [16]byte
		if _, err = rand.Read(nonce[:]); err != nil {
			return nil, err
		}
		j.instance = hex.EncodeToString(nonce[:])
		var tx *sql.Tx
		tx, err = j.db.Begin()
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		_, err = tx.Exec(`CREATE TABLE authority(instance TEXT PRIMARY KEY);
 CREATE TABLE admissions(id TEXT PRIMARY KEY,admission TEXT NOT NULL,phase TEXT NOT NULL,controller TEXT NOT NULL);
 CREATE TABLE helpers(admission_id TEXT NOT NULL REFERENCES admissions(id),kind TEXT NOT NULL,key TEXT NOT NULL,invocation TEXT NOT NULL,PRIMARY KEY(admission_id,kind,key));
 PRAGMA user_version=1;`)
		if err != nil {
			return nil, err
		}
		if _, err = tx.Exec(`INSERT INTO authority(instance) VALUES(?)`, j.instance); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		if err = syncDirectory(state); err != nil {
			return nil, err
		}
	}
	var version, count int
	if err = j.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return nil, err
	}
	if version != 1 {
		return nil, ErrAuthority
	}
	if err = j.db.QueryRow(`SELECT count(*) FROM authority`).Scan(&count); err != nil || count != 1 {
		if err == nil {
			err = ErrAuthority
		}
		return nil, err
	}
	if err = j.db.QueryRow(`SELECT instance FROM authority`).Scan(&j.instance); err != nil {
		return nil, err
	}
	if !id32.MatchString(j.instance) || instance != "" && j.instance != instance {
		return nil, ErrAuthority
	}
	if err = j.checkAuthority(); err != nil {
		return nil, err
	}
	if err = j.checkNames(); err != nil {
		return nil, err
	}
	return j, nil
}

func privateRegular(f *os.File, owner uint32) (os.FileInfo, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Sys().(*syscall.Stat_t).Uid != owner {
		return nil, ErrAuthority
	}
	return info, nil
}
func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func (j *Journal) checkNames() error {
	for _, check := range []struct {
		path string
		info os.FileInfo
	}{{j.state, j.directory}, {filepath.Join(j.state, databaseName), j.database}, {filepath.Join(j.state, lockName), j.lockIdentity}} {
		actual, err := os.Lstat(check.path)
		if err != nil {
			return err
		}
		if !os.SameFile(check.info, actual) || actual.Mode() != check.info.Mode() || actual.Sys().(*syscall.Stat_t).Uid != j.owner {
			return ErrAuthority
		}
	}
	return nil
}
func (j *Journal) flock(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := syscall.Flock(int(j.lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}
func (j *Journal) within(ctx context.Context, fn func() error) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return ErrAuthority
	}
	if err := j.flock(ctx); err != nil {
		return err
	}
	defer syscall.Flock(int(j.lock.Fd()), syscall.LOCK_UN)
	if err := j.checkNames(); err != nil {
		return err
	}
	if err := j.checkAuthority(); err != nil {
		return err
	}
	if err := fn(); err != nil {
		return err
	}
	return j.checkNames()
}

func (j *Journal) checkAuthority() error {
	var version, count int
	var instance string
	if err := j.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version != 1 {
		return ErrAuthority
	}
	if err := j.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`).Scan(&count); err != nil {
		return err
	}
	if count != 3 {
		return ErrAuthority
	}
	if err := j.db.QueryRow(`SELECT count(*) FROM authority`).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return ErrAuthority
	}
	if err := j.db.QueryRow(`SELECT instance FROM authority`).Scan(&instance); err != nil {
		return err
	}
	if instance != j.instance {
		return ErrAuthority
	}
	return nil
}
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	return errors.Join(j.db.Close(), j.lock.Close())
}
