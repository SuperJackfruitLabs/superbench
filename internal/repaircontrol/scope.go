package repaircontrol

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

// Scope only attaches admission checks. It cannot consume prepared authority,
// change the core lease, configure supervision or certify managed fencing.
func (j *Journal) Scope(ctx context.Context, a Admission, controller string) (context.Context, error) {
	if err := j.within(ctx, func() error { return j.running(a, controller) }); err != nil {
		return nil, err
	}
	scoped, err := operationcontrol.WithScope(ctx, a.Intent, func(fn func() error) error {
		return j.within(ctx, func() error {
			if err := j.running(a, controller); err != nil {
				return err
			}
			return fn()
		})
	})
	if err != nil {
		return nil, err
	}
	return operationcontrol.WithHelperRecorder(scoped, func(h operationcontrol.Helper) error {
		return j.within(ctx, func() error {
			if err := j.running(a, controller); err != nil {
				return err
			}
			return j.recordHelper(a, h)
		})
	})
}

func (j *Journal) recordHelper(a Admission, h operationcontrol.Helper) error {
	if !h.Valid(a.Intent) || h.State != a.Control.State {
		return ErrAuthority
	}
	raw, err := canonical(h)
	if err != nil {
		return err
	}
	var prior string
	err = j.db.QueryRow(`SELECT CASE WHEN length(invocation)<=? THEN invocation ELSE NULL END FROM helpers WHERE admission_id=? AND kind=? AND key=?`, MaxRecord, a.ID, h.Kind, h.Key).Scan(&prior)
	if err == nil {
		if prior != string(raw) {
			return ErrAuthority
		}
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	var count int
	if err = j.db.QueryRow(`SELECT count(*) FROM helpers WHERE admission_id=?`, a.ID).Scan(&count); err != nil {
		return err
	}
	if count >= operationcontrol.MaxHelpers {
		return ErrAuthority
	}
	result, err := j.db.Exec(`INSERT INTO helpers(admission_id,kind,key,invocation) VALUES(?,?,?,?)`, a.ID, h.Kind, h.Key, string(raw))
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return ErrAuthority
	}
	if err = j.db.QueryRow(`SELECT invocation FROM helpers WHERE admission_id=? AND kind=? AND key=?`, a.ID, h.Kind, h.Key).Scan(&prior); err != nil {
		return err
	}
	if prior != string(raw) {
		return ErrAuthority
	}
	return nil
}

// Helpers is the closed, initially tracked inventory, including meaningful
// empty inventories. It never adopts legacy/unbound helpers after a crash.
func (j *Journal) Helpers(ctx context.Context, a Admission) (out []operationcontrol.Helper, err error) {
	err = j.within(ctx, func() error {
		if _, err := j.get(a); err != nil {
			return err
		}
		rows, err := j.db.Query(`SELECT kind,key,CASE WHEN length(invocation)<=? THEN invocation ELSE NULL END FROM helpers WHERE admission_id=? ORDER BY kind,key`, MaxRecord, a.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if len(out) >= operationcontrol.MaxHelpers {
				return ErrAuthority
			}
			var kind, key, raw string
			if err := rows.Scan(&kind, &key, &raw); err != nil {
				return err
			}
			var h operationcontrol.Helper
			if json.Unmarshal([]byte(raw), &h) != nil || !h.Valid(a.Intent) || h.Kind != kind || h.Key != key || h.State != a.Control.State {
				return ErrAuthority
			}
			canonical, err := canonical(h)
			if err != nil || !bytes.Equal(canonical, []byte(raw)) {
				return ErrAuthority
			}
			out = append(out, h)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
