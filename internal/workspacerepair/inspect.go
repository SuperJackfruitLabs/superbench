package workspacerepair

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/initrepair"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/retention"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

type captureLock struct {
	id    string
	limit uint64
	file  *os.File
	entry *reap.Entry
	size  int64
}
type captureLocks []captureLock

func (l captureLocks) close() {
	for _, v := range l {
		if v.file != nil {
			v.file.Close()
		}
	}
}
func (l captureLocks) check(root string) error {
	for _, v := range l {
		if err := retention.RecheckInterrupted(root, v.id, v.limit, v.file, v.entry, v.size); err != nil {
			return err
		}
	}
	return nil
}

func (m Manager) Inspect(ctx context.Context, key string, rerunOnce []string) (*Plan, error) {
	ctx, err := m.controlled(ctx)
	if err != nil {
		return nil, err
	}
	if !requestID.MatchString(key) {
		return nil, errors.New("invalid initialization workspace key")
	}
	var out *Plan
	var locks captureLocks
	err = m.Prep.Gate(key, func() error {
		defer func() { locks.close() }()
		return m.Prep.Update(func(rows benchstate.Database, tx *sql.Tx) error {
			p, err := m.inspectState(ctx, rows, tx, key, rerunOnce, time.Now().UTC(), &locks)
			if err != nil {
				return err
			}
			if err = locks.check(m.Prep.State); err != nil {
				return err
			}
			out = &p
			return ctx.Err()
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (m Manager) inspectState(ctx context.Context, rows benchstate.Database, tx *sql.Tx, key string, choices []string, observed time.Time, locks *captureLocks) (Plan, error) {
	var p Plan
	b, err := stoppedOwner(rows, key)
	if err != nil {
		return p, err
	}
	if err = m.fence(b); err != nil {
		return p, err
	}
	if b.Lease.Execution != nil && b.Lease.Execution.Log != filepath.Join(m.Prep.State, "run-"+b.Lease.Execution.ID+".log") {
		return p, errors.New("initialization execution capture is outside registered state")
	}
	p = Plan{Version: Version, Root: m.Prep.State, DataRoot: m.Prep.Data, Key: key, BenchID: b.Workspace.ID, CreationID: b.Workspace.CreationID, Phase: b.Workspace.Phase, ObservedAt: observed}
	copy := workspace.Clone(b)
	p.Execution, p.Pending = copy.Lease.Execution, copy.Workspace.Pending
	p.BenchDigest, err = hash(b)
	if err != nil {
		return p, err
	}
	retired := map[string]bool{}
	if r := b.Workspace.InitializationRepair; r != nil {
		prior, err := initrepair.Get(tx, b.Workspace.ID, r.ID)
		if err != nil {
			return p, err
		}
		if prior.Key != key || prior.Digest != r.Digest || prior.PreviousID != r.PreviousID || prior.Status != "active" {
			return p, errors.New("initialization repair journal differs from its stopped owner")
		}
		p.PreviousID = r.ID
		for _, id := range r.RetiredRuns {
			retired[id] = true
		}
	}
	held := map[string]bool{}
	for _, id := range b.Lease.RetainedRuns {
		held[id] = true
	}
	if e := b.Lease.Execution; e != nil {
		held[e.ID] = true
	}
	var ids []string
	for id := range retired {
		ids = append(ids, id)
	}
	for id := range held {
		if retired[id] {
			return p, errors.New("retired initialization history cannot become live")
		}
		ids = append(ids, id)
	}
	if len(ids) > history.MaxRecords {
		return p, errors.New("initialization history exceeds admission bound")
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return p, err
		}
		r, err := history.Get(tx, id)
		if err != nil {
			return p, err
		}
		if r.Key != key || r.Holder != "initialization" || r.Origin != "supervised" || !r.ReleasedAt.IsZero() || r.SealedAt.IsZero() || r.Interruption == nil && (r.File == nil || r.ExitCode == nil) || r.Collection != "" || !r.CollectedAt.IsZero() {
			return p, errors.New("initialization history requires owned sealed or reviewed interrupted evidence")
		}
		h := HistoryBinding{ID: id, Retired: retired[id], Unknown: r.Interruption != nil, ExitCode: r.ExitCode, RecordedFile: r.File, RecordedSize: r.Size, Observed: !retired[id]}
		h.Digest, err = history.Digest(r)
		if err != nil {
			return p, err
		}
		if !retired[id] {
			limit := uint64(1 << 30)
			if e := b.Lease.Execution; e != nil && e.ID == id {
				limit = e.LogLimitBytes
			}
			f, entry, size, err := retention.ObserveInterrupted(m.Prep.State, id, limit)
			if err != nil {
				return p, err
			}
			*locks = append(*locks, captureLock{id: id, limit: limit, file: f, entry: entry, size: size})
			if r.File != nil && (entry == nil || *entry != *r.File || size != r.Size) {
				return p, errors.New("initialization captured history changed")
			}
			// A previously observed absent capture can have a late legacy artifact.
			// Bind it separately; never rewrite its unknown original observation.
			h.ObservedFile, h.ObservedSize = entry, size
		}
		p.History = append(p.History, h)
	}
	p.CurrentVector, err = m.Lifecycle.Inspect(ctx, b, false)
	if err != nil {
		return p, err
	}
	p.Members, err = m.Prep.InspectInitializationRepair(ctx, b, tx, choices)
	if err != nil {
		return p, err
	}
	p.Digest, err = p.hash()
	if err != nil {
		return p, err
	}
	_, err = p.Bytes()
	return p, err
}
