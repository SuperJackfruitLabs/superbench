package repaircontrol

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
)

func (j *Journal) get(a Admission) (Record, error) {
	var r Record
	if !a.Valid() || a.Instance != j.instance || a.Control.State != j.state {
		return r, ErrAuthority
	}
	var raw string
	err := j.db.QueryRow(`SELECT CASE WHEN length(admission)<=? THEN admission ELSE NULL END,phase,controller FROM admissions WHERE id=?`, MaxRecord, a.ID).Scan(&raw, &r.Phase, &r.Controller)
	if err != nil {
		return r, err
	}
	if json.Unmarshal([]byte(raw), &r.Admission) != nil || r.Admission != a || !r.valid() {
		return Record{}, ErrAuthority
	}
	if r.Phase == "prepared" {
		var helpers int
		if err := j.db.QueryRow(`SELECT count(*) FROM helpers WHERE admission_id=?`, a.ID).Scan(&helpers); err != nil {
			return Record{}, err
		}
		if helpers != 0 {
			return Record{}, ErrAuthority
		}
	}
	canonical, err := canonical(a)
	if err != nil || !bytes.Equal([]byte(raw), canonical) {
		return Record{}, ErrAuthority
	}
	return r, nil
}
func (j *Journal) Get(ctx context.Context, a Admission) (r Record, err error) {
	err = j.within(ctx, func() error { var e error; r, e = j.get(a); return e })
	return
}

// Reserve must precede the core claim commit. A failed core claim leaves only
// an inert prepared row; neither this row nor its presence may recreate core
// ownership. Existing terminal identities are never recycled or revived.
func (j *Journal) Reserve(ctx context.Context, a Admission) error {
	return j.within(ctx, func() error {
		if !a.Valid() || a.Instance != j.instance || a.Control.State != j.state {
			return ErrAuthority
		}
		old, err := j.get(a)
		if err == nil {
			if old.Phase == "prepared" {
				return nil
			}
			return ErrAuthority
		}
		if err != sql.ErrNoRows {
			return err
		}
		var count int
		if err = j.db.QueryRow(`SELECT count(*) FROM admissions`).Scan(&count); err != nil {
			return err
		}
		if count >= MaxRecords {
			return ErrAuthority
		}
		raw, err := canonical(a)
		if err != nil {
			return err
		}
		result, err := j.db.Exec(`INSERT INTO admissions(id,admission,phase,controller) VALUES(?,?,'prepared','')`, a.ID, string(raw))
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return ErrAuthority
		}
		_, err = j.get(a)
		return err
	})
}

// Start consumes the one prepared admission. A later controller finding running
// must reconcile this exact epoch and obtain a new reviewed claim, not replay.
func (j *Journal) Start(ctx context.Context, a Admission, controller string) error {
	if !id32.MatchString(controller) {
		return ErrAuthority
	}
	return j.within(ctx, func() error {
		r, err := j.get(a)
		if err != nil {
			return err
		}
		if r.Phase == "running" {
			return ErrRunning
		}
		if r.Phase != "prepared" {
			return ErrAuthority
		}
		return j.change(a, r, "running", controller)
	})
}

func (j *Journal) change(a Admission, old Record, phase, controller string) error {
	want := Record{Admission: a, Phase: phase, Controller: controller}
	if !want.valid() {
		return ErrAuthority
	}
	result, err := j.db.Exec(`UPDATE admissions SET phase=?,controller=? WHERE id=? AND phase=? AND controller=?`, phase, controller, a.ID, old.Phase, old.Controller)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return ErrAuthority
	}
	actual, err := j.get(a)
	if err != nil {
		return err
	}
	if actual != want {
		return ErrAuthority
	}
	return nil
}

func (j *Journal) running(a Admission, controller string) error {
	r, err := j.get(a)
	if err != nil {
		return err
	}
	if r.Phase == "cancel_requested" || r.Phase == "cancelled" {
		return ErrCancelled
	}
	if !id32.MatchString(controller) || r.Phase != "running" || r.Controller != controller {
		return ErrAuthority
	}
	return nil
}

// RequestCancel is only durable admission, never a fencing result. It takes no
// host/core lock or helper wait. Known rows remain cancellable at capacity.
func (j *Journal) RequestCancel(ctx context.Context, a Admission) error {
	return j.within(ctx, func() error {
		r, err := j.get(a)
		if err != nil {
			return err
		}
		if r.Phase == "cancel_requested" || r.Phase == "cancelled" {
			return nil
		}
		if r.Phase != "prepared" && r.Phase != "running" {
			return ErrAuthority
		}
		return j.change(a, r, "cancel_requested", r.Controller)
	})
}

func (j *Journal) Barrier(ctx context.Context, a Admission) error {
	return j.within(ctx, func() error {
		r, err := j.get(a)
		if err != nil {
			return err
		}
		if r.Phase != "cancel_requested" && r.Phase != "cancelled" {
			return ErrAuthority
		}
		return nil
	})
}

// Complete closes future effect admission before its caller may retire the core
// exclusion. The caller must first independently verify the same repair's
// durable ready completion, finished children and sealed histories. A crash
// after this write can only replay that cutoff, never restart the operation.
func (j *Journal) Complete(ctx context.Context, a Admission, controller string) error {
	return j.within(ctx, func() error {
		r, err := j.get(a)
		if err != nil {
			return err
		}
		if r.Phase == "completed" && r.Controller == controller {
			return nil
		}
		if err = j.running(a, controller); err != nil {
			return err
		}
		return j.change(a, r, "completed", controller)
	})
}

// ConfirmCancelled records only an independently concluded core cancellation.
// The caller must never use it as the proof supplied to core reconciliation.
func (j *Journal) ConfirmCancelled(ctx context.Context, a Admission) error {
	return j.within(ctx, func() error {
		r, err := j.get(a)
		if err != nil {
			return err
		}
		if r.Phase == "cancelled" {
			return nil
		}
		if r.Phase != "cancel_requested" {
			return ErrAuthority
		}
		return j.change(a, r, "cancelled", r.Controller)
	})
}
