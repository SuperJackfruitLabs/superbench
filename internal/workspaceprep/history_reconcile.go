package workspaceprep

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/retention"
)

func interruptedOwner(rows benchstate.Database, key string) (*model.Bench, error) {
	b := rows[key]
	if b == nil || b.Workspace == nil || b.Workspace.Archive != nil || b.Lease == nil || !b.Lease.StopRequested || b.Lease.Continuation != "" || b.Lease.Execution == nil || b.Lease.Execution.Phase != "stopped" {
		return nil, errors.New("administratively stopped workspace execution required")
	}
	return b, nil
}

func (m Manager) fenceHistory(b *model.Bench) error {
	e := b.Lease.Execution
	if e.Log != filepath.Join(m.State, "run-"+e.ID+".log") {
		return errors.New("workspace log path is not registered state capture")
	}
	if err := m.Backend.Fence(e); err != nil {
		return err
	}
	return m.Backend.Empty(e)
}

// InspectHistory requires an explicit stop first. It independently verifies the
// recorded scope and excludes any still-open product capturer. The resulting
// plan attests only the currently observed artifact (or absence).
func (m Manager) InspectHistory(key string) (*HistoryPlan, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	var out *HistoryPlan
	var locked *os.File
	err := m.Gate(key, func() error {
		defer func() {
			if locked != nil {
				locked.Close()
			}
		}()
		return m.Update(func(rows benchstate.Database, tx *sql.Tx) error {
			b, err := interruptedOwner(rows, key)
			if err != nil {
				return err
			}
			if err = m.fenceHistory(b); err != nil {
				return err
			}
			r, err := history.Get(tx, b.Lease.Execution.ID)
			if err != nil {
				return err
			}
			if r.Key != key || r.Holder != b.Lease.Holder || r.Origin != "supervised" || r.Interruption != nil || !r.ReleasedAt.IsZero() || !r.SealedAt.IsZero() || r.File != nil || r.ExitCode != nil || r.Size != 0 || r.Truncated || r.Collection != "" || !r.CollectedAt.IsZero() {
				return errors.New("unsealed held supervised history required")
			}
			p := HistoryPlan{Version: "superbench.workspace-history.v1", Root: m.State, Key: key, BenchID: b.Workspace.ID, Execution: *b.Lease.Execution, ObservedAt: time.Now().UTC()}
			p.PriorDigest, err = history.Digest(r)
			if err != nil {
				return err
			}
			p.BenchDigest, err = privateHistoryDigest(b)
			if err != nil {
				return err
			}
			locked, p.File, p.Size, err = retention.ObserveInterrupted(m.State, p.Execution.ID, p.Execution.LogLimitBytes)
			if err != nil {
				return err
			}
			p.Digest, err = p.hash()
			if err != nil {
				return err
			}
			if _, err = p.Bytes(); err != nil {
				return err
			}
			if err = retention.RecheckInterrupted(m.State, p.Execution.ID, p.Execution.LogLimitBytes, locked, p.File, p.Size); err != nil {
				return err
			}
			out = &p
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ReconcileHistory persists the exact reviewed unknown observation atomically.
// It never changes a lease, preparation proof, command receipt or task files.
func (m Manager) ReconcileHistory(p HistoryPlan) (history.Record, error) {
	var out history.Record
	if err := m.validate(); err != nil {
		return out, err
	}
	if err := p.validate(); err != nil {
		return out, err
	}
	if p.Root != m.State {
		return out, errors.New("workspace history plan belongs to another state root")
	}
	if p.ObservedAt.After(time.Now().UTC()) {
		return out, errors.New("workspace history observation is in the future")
	}
	var locked *os.File
	err := m.Gate(p.Key, func() error {
		defer func() {
			if locked != nil {
				locked.Close()
			}
		}()
		return m.Update(func(rows benchstate.Database, tx *sql.Tx) error {
			r, err := history.Get(tx, p.Execution.ID)
			if err != nil {
				return err
			}
			if r.Key != p.Key {
				return errors.New("workspace history belongs to another key")
			}
			if r.Interruption != nil {
				out, err = history.ObserveInterrupted(tx, p.Execution.ID, p.PriorDigest, p.observation(), p.File, p.Size)
				return err
			}
			b, err := interruptedOwner(rows, p.Key)
			if err != nil {
				return err
			}
			digest, err := privateHistoryDigest(b)
			if err != nil {
				return err
			}
			if b.Workspace.ID != p.BenchID || digest != p.BenchDigest || *b.Lease.Execution != p.Execution {
				return errors.New("workspace changed after history approval")
			}
			if r.Holder != b.Lease.Holder {
				return errors.New("workspace history owner changed")
			}
			if err = m.fenceHistory(b); err != nil {
				return err
			}
			var size int64
			var observedErr error
			var entry *reap.Entry
			locked, entry, size, observedErr = retention.ObserveInterrupted(m.State, p.Execution.ID, p.Execution.LogLimitBytes)
			// Keep the capture descriptor alive until the outer transaction commits.
			if observedErr != nil {
				return observedErr
			}
			if size != p.Size || (entry == nil) != (p.File == nil) || entry != nil && *entry != *p.File {
				return errors.New("workspace capture changed after approval")
			}
			out, err = history.ObserveInterrupted(tx, p.Execution.ID, p.PriorDigest, p.observation(), p.File, p.Size)
			if err != nil {
				return err
			}
			if err = m.Backend.Empty(&p.Execution); err != nil {
				return err
			}
			return retention.RecheckInterrupted(m.State, p.Execution.ID, p.Execution.LogLimitBytes, locked, p.File, p.Size)
		})
	})
	if err != nil {
		return history.Record{}, err
	}
	return out, nil
}
