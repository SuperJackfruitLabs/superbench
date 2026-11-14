package bench

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var keyPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$`)

type Options struct{ Repo, Branch, Base, Holder, Token string }

func (s Store) Create(key string, o Options, recovering bool) (*Bench, error) {
	var result *Bench
	err := s.transaction(func(db database) error {
		if !keyPattern.MatchString(key) {
			return errors.New("key must be 1-80 letters, digits, underscores or hyphens, starting with a letter or digit")
		}
		if db[key] != nil {
			return fmt.Errorf("%w; recovery uses a new host/state or a new key", ErrExists)
		}
		if o.Repo == "" || strings.HasPrefix(o.Repo, "-") {
			return errors.New("a repository is required")
		}
		if o.Branch == "" {
			o.Branch = "bench/" + key
		}
		if _, err := git(s.State, "check-ref-format", "--branch", o.Branch); err != nil {
			return err
		}
		if o.Base == "" {
			o.Base = "main"
		}
		if _, err := git(s.State, "check-ref-format", "--branch", o.Base); err != nil {
			return err
		}
		pending, err := s.pending(key, o, recovering)
		if err != nil {
			return err
		}
		if e := s.admit(); e != nil {
			return e
		}
		if pending != nil {
			if err = s.applyBenchQuota(pending); err != nil {
				return err
			}
			result, err = finishCreate(pending)
			if err != nil {
				return err
			}
			db[key] = result
			return nil
		}
		if _, err = os.Lstat(filepath.Join(s.Data, "benches", key)); err == nil {
			return errors.New("unregistered checkout has no creation journal; refusing adoption")
		} else if !os.IsNotExist(err) {
			return err
		}
		mirror, err := s.prepareMirror(o.Repo)
		if err != nil {
			return err
		}

		branchRef := "refs/remotes/origin/" + o.Branch
		_, remoteErr := git(mirror, "rev-parse", "--verify", branchRef)
		if recovering && remoteErr != nil {
			return errors.New("recovery requires an existing remote task branch")
		}
		source := branchRef
		if remoteErr != nil {
			source = "refs/remotes/origin/" + o.Base
		}
		sha, err := git(mirror, "rev-parse", "--verify", source+"^{commit}")
		if err != nil {
			return err
		}
		b := Bench{Key: key, Repo: o.Repo, Branch: o.Branch, Path: filepath.Join(s.Data, "benches", key), Mirror: mirror}
		if remoteErr == nil {
			b.Checkpoint = sha
		}
		intent := creation{Bench: b, Source: sha, Base: o.Base, Recovering: recovering}
		if err = s.record(intent); err != nil {
			return err
		}
		if err = s.applyBenchQuota(&intent); err != nil {
			return err
		}
		result, err = finishCreate(&intent)
		if err != nil {
			return err
		}

		db[key] = result
		return nil
	})
	return result, err
}
func get(db database, key string) (*Bench, error) {
	b := db[key]
	if b == nil {
		return nil, ErrNotFound
	}
	return b, nil
}
func owned(b *Bench, token string) error {
	if b.Lease == nil || token == "" || b.Lease.Token != token {
		return ErrLease
	}
	return nil
}
func (s Store) Lease(key, holder string) (*Bench, error) {
	var result *Bench
	err := s.transaction(func(db database) error {
		if e := s.admit(); e != nil {
			return e
		}
		b, err := get(db, key)
		if err != nil {
			return err
		}
		if b.Lease != nil {
			return fmt.Errorf("%w by %s; no automatic expiry", ErrBusy, b.Lease.Holder)
		}
		lease, err := newLease(holder)
		if err != nil {
			return err
		}
		b.Lease = lease

		result = b
		return nil
	})
	return result, err
}
func (s Store) Checkpoint(key, token string) (*Bench, error) {
	var result *Bench
	err := s.transaction(func(db database) error {
		b, err := get(db, key)
		if err != nil {
			return err
		}
		if err = owned(b, token); err != nil {
			return err
		}
		if err = s.ensureStopped(b); err != nil {
			return err
		}
		if err = clean(b); err != nil {
			return err
		}
		sha, err := head(b)
		if err != nil {
			return err
		}
		if _, err = git(b.Path, "push", "origin", "HEAD:refs/heads/"+b.Branch); err != nil {
			return err
		}
		if err = confirmed(b, sha); err != nil {
			return err
		}
		b.Checkpoint = sha
		copy := *b
		copy.Lease = publicLease(b.Lease)
		result = &copy
		return nil
	})
	return result, err
}
func (s Store) Release(key, token string) error {
	return s.transaction(func(db database) error {
		b, err := get(db, key)
		if err != nil {
			return err
		}
		if err = owned(b, token); err != nil {
			return err
		}
		if err = s.ensureStopped(b); err != nil {
			return err
		}
		if err = clean(b); err != nil {
			return err
		}
		sha, err := head(b)
		if err != nil {
			return err
		}
		if b.Checkpoint == "" || b.Checkpoint != sha {
			return ErrCheckpoint
		}
		if err = confirmed(b, sha); err != nil {
			return err
		}
		b.Lease = nil
		return nil
	})
}
func (s Store) Status(key string) ([]Bench, error) {
	var result []Bench
	err := s.read(func(db database) error {
		if key != "" {
			b, err := get(db, key)
			if err != nil {
				return err
			}
			copy := *b
			copy.Lease = publicLease(b.Lease)
			result = append(result, copy)
		} else {
			for _, b := range db {
				copy := *b
				copy.Lease = publicLease(b.Lease)
				result = append(result, copy)
			}
		}
		return nil
	})
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result, err
}
func publicLease(l *Lease) *Lease {
	if l == nil {
		return nil
	}
	copy := *l
	copy.Token = ""
	return &copy
}
