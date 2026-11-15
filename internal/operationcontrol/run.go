package operationcontrol

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

// A request can retain at most the lease's bounded run vector and current run.
const MaxRuns = model.WorkspaceRetainedRuns + 1

type Run struct {
	Execution   model.Execution
	PriorDigest string
}

func runSchema(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS workspace_operation_runs(operation_id TEXT NOT NULL,epoch TEXT NOT NULL,run_id TEXT PRIMARY KEY,scope TEXT NOT NULL,history_digest TEXT NOT NULL)`)
	return err
}

func validRun(v Run, c Control) bool {
	e := v.Execution
	if !hex32.MatchString(e.ID) || e.Phase != "starting" || e.PID != 0 || !hex64.MatchString(e.HostID) || e.BootID == "" || len(e.BootID) > 128 || strings.ContainsAny(e.BootID, "\x00\r\n\t ") || !filepath.IsAbs(e.Root) || filepath.Clean(e.Root) != e.Root || e.LogLimitBytes < 1024 || e.LogLimitBytes > 1<<30 || !hex64.MatchString(v.PriorDigest) {
		return false
	}
	if c.Managed() && (e.Root != c.Root || e.HostID != c.HostID || e.BootID != c.BootID || e.Log != filepath.Join(c.State, "run-"+e.ID+".log")) {
		return false
	}
	return true
}

// StartHistory commits scope provenance in the SAME transaction as the existing
// supervised history intent. Scope creation may have occurred, but Start and
// capture-open must remain after this transaction commits. No earlier run is
// retrospectively attributed to this request.
func StartHistory(ctx context.Context, tx *sql.Tx, b *model.Bench, r history.Record) error {
	i, scoped := FromContext(ctx)
	if !scoped {
		return history.Start(tx, r)
	}
	if err := GuardContext(ctx, tx, b); err != nil {
		return err
	}
	if b.Lease.Execution == nil || r.ID != b.Lease.Execution.ID || r.Key != b.Key || r.Holder != b.Lease.Holder {
		return ErrAuthority
	}
	var count int
	if err := tx.QueryRow(`SELECT count(*) FROM workspace_operation_runs WHERE operation_id=?`, i.Target.ID()).Scan(&count); err != nil {
		return err
	}
	if count >= MaxRuns {
		return ErrBusy
	}
	if err := history.Start(tx, r); err != nil {
		return err
	}
	saved, err := history.Get(tx, r.ID)
	if err != nil {
		return err
	}
	digest, err := history.Digest(saved)
	if err != nil {
		return err
	}
	v := Run{Execution: *b.Lease.Execution, PriorDigest: digest}
	if !validRun(v, ControlFromContext(ctx)) {
		return ErrAuthority
	}
	raw, err := json.Marshal(v.Execution)
	if err != nil {
		return err
	}
	result, err := tx.Exec(`INSERT INTO workspace_operation_runs(operation_id,epoch,run_id,scope,history_digest) VALUES(?,?,?,?,?)`, i.Target.ID(), i.Epoch, r.ID, string(raw), digest)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrAuthority
	}
	got, err := GetRun(tx, i, r.ID)
	if err != nil {
		return err
	}
	if got != v {
		return ErrAuthority
	}
	return nil
}

func GetRun(r Reader, i Intent, id string) (Run, error) {
	var v Run
	if !i.Valid() || !hex32.MatchString(id) {
		return v, ErrAuthority
	}
	b, err := Get(r, i.Target)
	if err != nil {
		return v, err
	}
	if b.Intent != i {
		return v, ErrAuthority
	}
	var op, epoch, raw string
	if err = r.QueryRow(`SELECT operation_id,epoch,scope,history_digest FROM workspace_operation_runs WHERE run_id=?`, id).Scan(&op, &epoch, &raw, &v.PriorDigest); err != nil {
		return v, err
	}
	if op != i.Target.ID() || epoch != i.Epoch || len(raw) > 4096 {
		return v, ErrAuthority
	}
	if err = json.Unmarshal([]byte(raw), &v.Execution); err != nil {
		return v, ErrAuthority
	}
	canonical, err := json.Marshal(v.Execution)
	if err != nil || !bytes.Equal([]byte(raw), canonical) || v.Execution.ID != id || !validRun(v, b.Control) {
		return v, ErrAuthority
	}
	return v, nil
}
