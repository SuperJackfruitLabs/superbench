package initrepair

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repaircontrol"
)

func OperationSchema(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS initialization_repair_operations (
 bench_id TEXT NOT NULL,id TEXT NOT NULL,admission TEXT NOT NULL,
 PRIMARY KEY(bench_id,id),FOREIGN KEY(bench_id,id) REFERENCES initialization_repairs(bench_id,id));`)
	return err
}

// BindOperation shares the transaction saving the fresh lease, repair journal
// and operation binding. The caller first reserves the independent sidecar row;
// no later lookup is allowed to reconstruct that absent admission as tracked.
func BindOperation(tx *sql.Tx, a repaircontrol.Admission) error {
	if err := validateOperation(tx, a); err != nil {
		return err
	}
	j, err := Get(tx, a.BenchID, a.RepairID)
	if err != nil {
		return err
	}
	if j.Status != "active" {
		return errors.New("terminal initialization repair cannot bind an operation")
	}
	prior, err := GetOperation(tx, a.BenchID, a.RepairID)
	if err == nil {
		if prior == a {
			return nil
		}
		return errors.New("initialization repair operation is immutable")
	}
	if err != sql.ErrNoRows {
		return err
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	result, err := tx.Exec(`INSERT INTO initialization_repair_operations(bench_id,id,admission) VALUES(?,?,?)`, a.BenchID, a.RepairID, string(raw))
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return operationcontrol.ErrAuthority
	}
	actual, err := GetOperation(tx, a.BenchID, a.RepairID)
	if err != nil {
		return err
	}
	if actual != a {
		return operationcontrol.ErrAuthority
	}
	return nil
}

func validateOperation(r Reader, a repaircontrol.Admission) error {
	if !a.Valid() {
		return operationcontrol.ErrAuthority
	}
	j, err := Get(r, a.BenchID, a.RepairID)
	if err != nil {
		return err
	}
	if j.Key != a.Intent.Key || j.Digest != a.Approval || j.OwnerDigest != a.OwnerDigest {
		return operationcontrol.ErrAuthority
	}
	b, err := operationcontrol.Get(r, a.Intent.Target)
	if err != nil {
		return err
	}
	if b.Intent != a.Intent || b.Control != a.Control || b.BenchID != a.BenchID || b.Holder != "initialization" || b.TokenHash != a.OwnerDigest || b.Phase == "admitted" {
		return operationcontrol.ErrAuthority
	}
	return nil
}

func GetOperation(r Reader, benchID, id string) (repaircontrol.Admission, error) {
	var a repaircontrol.Admission
	if !identity.MatchString(benchID) || !requestID.MatchString(id) {
		return a, operationcontrol.ErrAuthority
	}
	var raw string
	if err := r.QueryRow(`SELECT CASE WHEN length(admission)<=? THEN admission ELSE NULL END FROM initialization_repair_operations WHERE bench_id=? AND id=?`, repaircontrol.MaxRecord, benchID, id).Scan(&raw); err != nil {
		return a, err
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return a, err
	}
	canonical, err := json.Marshal(a)
	if err != nil || !bytes.Equal(canonical, []byte(raw)) || a.BenchID != benchID || a.RepairID != id {
		return repaircontrol.Admission{}, operationcontrol.ErrAuthority
	}
	if err := validateOperation(r, a); err != nil {
		return repaircontrol.Admission{}, err
	}
	return a, nil
}

func RejectOperationAuthority(r Reader) error {
	var count int
	if err := r.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='initialization_repair_operations'`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return operationcontrol.ErrAuthority
	}
	if err := r.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='workspace_operations'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	if err := r.QueryRow(`SELECT count(*) FROM workspace_operations WHERE operation='initialization-repair'`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return operationcontrol.ErrAuthority
	}
	return nil
}
