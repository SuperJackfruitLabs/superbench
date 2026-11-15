// Package operationcancel reconciles durable private operation authority. It
// does not itself acknowledge transport cancellation or grant capabilities.
package operationcancel

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/retention"
)

type Backend interface {
	Fence(*model.Execution) error
	Empty(*model.Execution) error
}

type Manager struct {
	State   string
	Backend Backend
	// Gate excludes starts and ownership changes. Update must hold the host lock
	// through commit, with selective operation recovery, never global Git recovery
	// while an unrelated operation is live. The durable cancel barrier precedes it.
	Gate   func(string, func() error) error
	Update func(func(benchstate.Database, *sql.Tx) error) error
}

// ReconcileInterruption preserves a partial/absent capture from an exact run
// precommitted by this operation. It does not adopt prior unlabelled executions,
// replace actual sealed outcomes, release ownership, or record hook success.
func (m Manager) ReconcileInterruption(i operationcontrol.Intent) error {
	if !i.Valid() || m.Backend == nil || m.Gate == nil || m.Update == nil {
		return operationcontrol.ErrAuthority
	}
	return m.Gate(i.Key, func() error {
		var locked *os.File
		defer func() {
			if locked != nil {
				_ = locked.Close()
			}
		}()
		return m.Update(func(rows benchstate.Database, tx *sql.Tx) error {
			b := rows[i.Key]
			if err := operationcontrol.RequireRevoked(tx, i, b); err != nil {
				return err
			}
			binding, err := operationcontrol.Get(tx, i.Target)
			if err != nil {
				return err
			}
			if !binding.Control.Managed() || binding.Control.State != m.State || b.Lease.Execution == nil {
				return operationcontrol.ErrAuthority
			}
			e := b.Lease.Execution
			run, err := operationcontrol.GetRun(tx, i, e.ID)
			if err != nil {
				return err
			}
			expected := run.Execution
			actual := *e
			actual.Phase = expected.Phase
			actual.PID = expected.PID
			if actual != expected {
				return operationcontrol.ErrAuthority
			}
			r, err := history.Get(tx, e.ID)
			if err != nil {
				return err
			}
			if r.Key != b.Key || r.Holder != b.Lease.Holder || r.Origin != "supervised" || !r.ReleasedAt.IsZero() || r.Collection != "" || !r.CollectedAt.IsZero() {
				return operationcontrol.ErrAuthority
			}
			prior, err := history.Digest(r)
			if err != nil {
				return err
			}
			if r.SealedAt.IsZero() && prior != run.PriorDigest {
				return operationcontrol.ErrAuthority
			}
			if err = m.Backend.Fence(e); err != nil {
				return err
			}
			if err = m.Backend.Empty(e); err != nil {
				return err
			}
			if !r.SealedAt.IsZero() {
				if r.File == nil && r.Interruption == nil {
					return errors.New("history has no sealed capture or interruption evidence")
				}
				return nil
			}
			var entry *reap.Entry
			var size int64
			locked, entry, size, err = retention.ObserveInterrupted(m.State, e.ID, e.LogLimitBytes)
			if err != nil {
				return err
			}
			now := time.Now().UTC()
			raw, err := json.Marshal(struct {
				Domain  string
				Intent  operationcontrol.Intent
				BenchID string
				Run     operationcontrol.Run
				At      time.Time
				File    *reap.Entry
				Size    int64
			}{"superbench.operation-cancel-interruption.v1", i, b.Workspace.ID, run, now, entry, size})
			if err != nil {
				return err
			}
			sum := sha256.Sum256(raw)
			state := "absent"
			if entry != nil {
				state = "present"
			}
			observation := history.InterruptedObservation{ApprovalDigest: hex.EncodeToString(sum[:]), PriorDigest: prior, LogState: state, ObservedAt: now}
			if _, err = history.ObserveInterrupted(tx, e.ID, prior, observation, entry, size); err != nil {
				return err
			}
			if err = m.Backend.Empty(e); err != nil {
				return err
			}
			return retention.RecheckInterrupted(m.State, e.ID, e.LogLimitBytes, locked, entry, size)
		})
	})
}
