package bench

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/archivepolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/setup"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var keyPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$`)

type Options = model.Options

func (s Store) createRegistered(ctx context.Context, key string, o Options, recovering, initialize bool) (*Bench, bool, error) {
	var warming bool
	var selectionErr error
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
		if _, err := s.git(s.State, "check-ref-format", "--branch", o.Branch); err != nil {
			return err
		}
		if o.Base == "" {
			o.Base = "main"
		}
		if _, err := s.git(s.State, "check-ref-format", "--branch", o.Base); err != nil {
			return err
		}
		pending, err := s.pending(key, o, recovering)
		if err != nil {
			return err
		}
		if e := s.admit(); e != nil {
			return e
		}
		publish := func(b *Bench) error {
			result = b
			if initialize {
				warming, selectionErr = s.creationSetupNeeded(b)
				if warming || selectionErr != nil {
					lease, e := newLease("initialization")
					if e != nil {
						return e
					}
					lease.Continuation, e = nonce()
					if e != nil {
						return e
					}
					b.Lease = lease
				}
			}
			db[key] = b
			return nil
		}
		if pending != nil {
			if !pending.QuotaRequestPinned || pending.Bench.CreatedFrom == "" {
				// Even without a recorded reply, an older helper may have allocated
				// its default cap. Never reinterpret a legacy creation request.
				if !pending.QuotaRequestPinned {
					pending.Bench.RequestedQuotaBytes = 0
				}
				pending.Bench.CreatedFrom = pending.Source
				pending.QuotaRequestPinned = true
				if err = s.record(*pending); err != nil {
					return err
				}
			}
			if err = s.applyBenchQuota(pending); err != nil {
				return err
			}
			result, err = s.finishCreate(pending)
			if err != nil {
				return err
			}
			return publish(result)
		}
		if _, err = os.Lstat(filepath.Join(s.Data, "benches", key)); err == nil {
			return errors.New("unregistered checkout has no creation journal; refusing adoption")
		} else if !os.IsNotExist(err) {
			return err
		}
		mirror, err := s.prepareMirror(ctx, o.Repo)
		if err != nil {
			return err
		}

		branchRef := "refs/remotes/origin/" + o.Branch
		_, remoteErr := s.git(mirror, "rev-parse", "--verify", branchRef)
		if recovering && remoteErr != nil {
			return errors.New("recovery requires an existing remote task branch")
		}
		source := branchRef
		if remoteErr != nil {
			source = "refs/remotes/origin/" + o.Base
		}
		sha, err := s.git(mirror, "rev-parse", "--verify", source+"^{commit}")
		if err != nil {
			return err
		}
		requested, err := setup.QuotaAtContext(ctx, mirror, sha)
		if err != nil {
			return err
		}
		b := Bench{CreatedFrom: sha, RequestedQuotaBytes: requested, Key: key, Repo: o.Repo, Branch: o.Branch, Path: filepath.Join(s.Data, "benches", key), Mirror: mirror}
		if remoteErr == nil {
			b.Checkpoint = sha
		}
		intent := creation{QuotaRequestPinned: true, Bench: b, Source: sha, Base: o.Base, Recovering: recovering}
		if err = s.record(intent); err != nil {
			return err
		}
		if err = s.applyBenchQuota(&intent); err != nil {
			return err
		}
		result, err = s.finishCreate(&intent)
		if err != nil {
			return err
		}

		return publish(result)
	})
	if err != nil {
		return result, false, err
	}
	return result, warming, selectionErr
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
func (s Store) acquireLease(ctx context.Context, key, holder string, prepare bool) (*Bench, *setup.Config, error) {
	var config *setup.Config
	var result *Bench
	err := s.transactionSQL(func(db database, tx *sql.Tx) error {
		if e := s.admit(); e != nil {
			return e
		}
		b, err := get(db, key)
		if err != nil {
			return err
		}
		if b.ArchivedAt != "" {
			return ErrArchived
		}
		if b.Lease != nil {
			return fmt.Errorf("%w by %s; no automatic expiry", ErrBusy, b.Lease.Holder)
		}
		if e := s.ensureBenchQuota(b); e != nil {
			return e
		}
		lease, err := newLease(holder)
		if err != nil {
			return err
		}
		if prepare {
			if err = s.refreshMirror(ctx, b); err != nil {
				return err
			}
			config, err = setup.Read(b.Path)
			if err != nil {
				return err
			}
			if err = setup.MatchQuota(config, b.RequestedQuotaBytes); err != nil {
				return err
			}
			if config != nil && config.Hooks() {
				if s.executor == nil && !execution.Supported {
					return errors.New("configured preparation requires Linux cgroup fencing on this host")
				}
				lease.Continuation, err = nonce()
				if err != nil {
					return err
				}
			} else {
				config = nil
				if err = setup.Invalidate(tx, key); err != nil {
					return err
				}
			}
		}
		b.Lease = lease

		result = b
		return nil
	})
	return result, config, err
}
func (s Store) Checkpoint(key, token string) (*Bench, error) {
	s, controlErr := s.reconciliationControls(key)
	if controlErr != nil {
		return nil, controlErr
	}
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
		if err = s.clean(b); err != nil {
			return err
		}
		sha, err := s.head(b)
		if err != nil {
			return err
		}
		if _, err = s.git(b.Path, "push", "origin", "HEAD:refs/heads/"+b.Branch); err != nil {
			return err
		}
		if err = s.confirmed(b, sha); err != nil {
			return err
		}
		b.Checkpoint = sha
		archivepolicy.NoteCheckpoint(s.controlContext(), s.mirrors(), b)
		copy := *b
		copy.Lease = publicLease(b.Lease)
		result = &copy
		return nil
	})
	return result, err
}
func (s Store) Release(key, token string) error {
	return s.release(key, token, false)
}
func (s Store) release(key, token string, automatic bool) error {
	s, controlErr := s.reconciliationControls(key)
	if controlErr != nil {
		return controlErr
	}
	return s.transactionSQL(func(db database, tx *sql.Tx) error {
		b, err := get(db, key)
		if err != nil {
			return err
		}
		if err = owned(b, token); err != nil {
			return err
		}
		if automatic && b.Lease.StopRequested {
			return errors.New("operator stop retained the lease; reconcile explicitly")
		}
		if err = s.ensureStopped(b); err != nil {
			return err
		}
		if err = s.clean(b); err != nil {
			return err
		}
		sha, err := s.head(b)
		if err != nil {
			return err
		}
		if b.Checkpoint == "" || b.Checkpoint != sha {
			return ErrCheckpoint
		}
		if err = s.confirmed(b, sha); err != nil {
			return err
		}
		if b.Lease.Execution != nil {
			if err = history.Released(tx, b.Lease.Execution.ID, b.Checkpoint); err != nil {
				return err
			}
		}
		for _, id := range b.Lease.RetainedRuns {
			if _, err = history.Get(tx, id); err != nil {
				return err
			}
			if err = history.Released(tx, id, b.Checkpoint); err != nil {
				return err
			}
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
	copy.Continuation = ""
	return &copy
}
