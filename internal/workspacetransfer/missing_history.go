package workspacetransfer

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/retention"
)

// TransferHistory binds retained evidence without declaring that its work was
// checkpointed. File is the current observation; collected historical captures
// remain collected and are never recreated or adopted here.
type TransferHistory struct {
	ID, Digest string
	Limit      uint64
	File       *reap.Entry
	Size       int64
}

type transferCapture struct {
	observation TransferHistory
	file        *os.File
}
type transferCaptures []transferCapture

func (c transferCaptures) close() {
	for _, capture := range c {
		if capture.file != nil {
			_ = capture.file.Close()
		}
	}
}
func (c transferCaptures) check(root string) error {
	for _, capture := range c {
		o := capture.observation
		if err := retention.RecheckInterrupted(root, o.ID, o.Limit, capture.file, o.File, o.Size); err != nil {
			return err
		}
	}
	return nil
}

// snapshotTransferHistory is read-only. The enclosing workspace/host exclusion
// and independently verified fence must remain held through the SQL commit.
// An unsealed held run needs the existing explicit unknown-history review first.
func snapshotTransferHistory(ctx context.Context, root string, b *model.Bench, tx *sql.Tx, locks *transferCaptures) ([]TransferHistory, error) {
	ids, held := map[string]bool{}, map[string]bool{}
	if b.Lease != nil {
		for _, id := range b.Lease.RetainedRuns {
			ids[id], held[id] = true, true
		}
		if e := b.Lease.Execution; e != nil {
			if e.Phase != "stopped" || e.Log != filepath.Join(root, "run-"+e.ID+".log") {
				return nil, errors.New("missing-source history requires the exact stopped execution capture")
			}
			ids[e.ID], held[e.ID] = true, true
		}
	}
	for _, proof := range b.Workspace.Preparation {
		if proof.RunID != "" {
			ids[proof.RunID] = true
		}
	}
	if repair := b.Workspace.InitializationRepair; repair != nil {
		for _, id := range repair.RetiredRuns {
			ids[id] = true
		}
	}
	if len(ids) > history.MaxRecords {
		return nil, errors.New("missing-source history exceeds the protected evidence bound")
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	out := make([]TransferHistory, 0, len(ordered))
	for _, id := range ordered {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r, err := history.Get(tx, id)
		if err != nil {
			return nil, err
		}
		if r.Key != b.Key || r.Origin != "supervised" || r.SealedAt.IsZero() || r.Interruption == nil && (r.File == nil || r.ExitCode == nil) || r.Collection != "" && r.CollectedAt.IsZero() {
			return nil, errors.New("source history requires genuine sealed or explicitly reviewed unknown evidence")
		}
		if held[id] && (b.Lease == nil || r.Holder != b.Lease.Holder || !r.ReleasedAt.IsZero() || !r.CollectedAt.IsZero()) {
			return nil, errors.New("held source history changed owner or was released/collected")
		}
		o := TransferHistory{ID: id, Limit: 1 << 30}
		if b.Lease != nil && b.Lease.Execution != nil && b.Lease.Execution.ID == id {
			o.Limit = b.Lease.Execution.LogLimitBytes
		}
		o.Digest, err = hash(r)
		if err != nil {
			return nil, err
		}
		f, entry, size, err := retention.ObserveInterrupted(root, id, o.Limit)
		if err != nil {
			return nil, err
		}
		o.File, o.Size = entry, size
		*locks = append(*locks, transferCapture{observation: o, file: f})
		if r.CollectedAt.IsZero() && r.File != nil && (entry == nil || *entry != *r.File || size != r.Size) {
			return nil, errors.New("source history capture changed after sealing")
		}
		out = append(out, o)
	}
	return out, locks.check(root)
}
