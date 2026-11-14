// Package bench implements a local, cooperative single-writer prototype.
package bench

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type Lease struct {
	Token  string
	Holder string
}
type Bench struct {
	Key        string
	Repo       string
	Branch     string
	Path       string
	Mirror     string
	Checkpoint string
	Lease      *Lease `json:",omitempty"`
}
type Store struct{ State, Data string }
type database map[string]*Bench

func (s Store) validate() error {
	for _, p := range []string{s.State, s.Data} {
		if !filepath.IsAbs(p) {
			return errors.New("state and data paths must be absolute")
		}
	}
	// Canonicalize existing ancestors as well as existing directories to reject symlink overlap.
	canonical := func(p string) (string, error) {
		var tail []string
		for {
			resolved, err := filepath.EvalSymlinks(p)
			if err == nil {
				for i := len(tail) - 1; i >= 0; i-- {
					resolved = filepath.Join(resolved, tail[i])
				}
				return resolved, nil
			}
			if !os.IsNotExist(err) {
				return "", err
			}
			tail = append(tail, filepath.Base(p))
			p = filepath.Dir(p)
		}
	}
	state, err := canonical(s.State)
	if err != nil {
		return err
	}
	data, err := canonical(s.Data)
	if err != nil {
		return err
	}
	for _, pair := range [][2]string{{state, data}, {data, state}} {
		rel, e := filepath.Rel(pair[0], pair[1])
		if e != nil {
			return e
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return errors.New("state and data directories must be separate, non-nested paths")
		}
	}
	return nil
}

func (s Store) transaction(fn func(database) error) error {
	if err := s.validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.State, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(s.State, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) // process exit also releases the lock
	db := database{}
	raw, err := os.ReadFile(filepath.Join(s.State, "benches.json"))
	if err == nil {
		if err = json.Unmarshal(raw, &db); err != nil {
			return fmt.Errorf("invalid state: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if db == nil {
		return errors.New("invalid state: null database")
	}
	for key, b := range db {
		if b == nil || b.Key != key || b.Path == "" || b.Mirror == "" {
			return errors.New("invalid bench record")
		}
	}
	if err = fn(db); err != nil {
		return err
	}
	raw, err = json.MarshalIndent(db, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(s.State, filepath.Join(s.State, "benches.json"), raw)
}

func atomicWrite(dirPath, target string, raw []byte) error {
	tmp, err := os.CreateTemp(dirPath, "state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), target); err != nil {
		return err
	}
	dir, err := os.Open(dirPath)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
