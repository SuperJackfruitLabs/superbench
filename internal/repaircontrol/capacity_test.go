package repaircontrol

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestTerminalHistoryDoesNotExhaustRepairAdmissionBudget(t *testing.T) {
	j, template := fixture(t)
	ctx := context.Background()
	controller := strings.Repeat("8", 32)
	tx, err := j.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var oldest, latest Admission
	for n := 0; n <= MaxRecords; n++ {
		a := template
		a.ID = fmt.Sprintf("%032x", n+1)
		a.Intent.Target.RequestID = fmt.Sprintf("finished-%d", n)
		a.Intent.Epoch = fmt.Sprintf("%032x", n+1)
		raw, err := canonical(a)
		if err != nil {
			t.Fatal(err)
		}
		phase := "completed"
		if n == MaxRecords {
			phase = "cancelled"
		}
		if _, err := tx.Exec(`INSERT INTO admissions(id,admission,phase,controller) VALUES(?,?,?,?)`, a.ID, string(raw), phase, controller); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			oldest = a
		}
		latest = a
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	fresh := template
	fresh.ID = strings.Repeat("f", 32)
	fresh.Intent.Target.RequestID = "new-after-history"
	if err := j.Reserve(ctx, fresh); err != nil {
		t.Fatal("terminal history exhausted repair admission budget", err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(template.Control.State, template.Instance)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, prior := range []struct {
		admission Admission
		phase     string
		refusal   error
	}{{oldest, "completed", ErrAuthority}, {latest, "cancelled", ErrCancelled}} {
		r, err := reopened.Get(ctx, prior.admission)
		if err != nil || r != (Record{Admission: prior.admission, Phase: prior.phase, Controller: controller}) {
			t.Fatal("historical repair changed across reopen", r, err)
		}
		if err := reopened.Reserve(ctx, prior.admission); !errors.Is(err, ErrAuthority) {
			t.Fatal("historical repair identity could be reserved again", err)
		}
		if _, err := reopened.Scope(ctx, prior.admission, controller); !errors.Is(err, prior.refusal) {
			t.Fatal("historical repair regained effect authority", err)
		}
		changed := prior.admission
		changed.Approval = strings.Repeat("e", 64)
		if _, err := reopened.Get(ctx, changed); !errors.Is(err, ErrAuthority) {
			t.Fatal("changed historical approval was accepted", err)
		}
	}
	r, err := reopened.Get(ctx, fresh)
	if err != nil || r != (Record{Admission: fresh, Phase: "prepared"}) {
		t.Fatal("new inert reservation changed across reopen", r, err)
	}
	var retained int
	if err := reopened.db.QueryRow(`SELECT count(*) FROM admissions`).Scan(&retained); err != nil || retained != MaxRecords+2 {
		t.Fatal("historical identities were removed to free capacity", retained, err)
	}
}

func TestRepairTerminalCutoffsReleaseOnlyTheirOwnAdmissionSlots(t *testing.T) {
	for _, cutoff := range []string{"completed", "cancelled"} {
		t.Run(cutoff, func(t *testing.T) {
			j, a := fixture(t)
			ctx := context.Background()
			controller := strings.Repeat("8", 32)
			if err := j.Reserve(ctx, a); err != nil {
				t.Fatal(err)
			}
			tx, err := j.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			for n := 1; n < MaxRecords; n++ {
				other := a
				other.ID = fmt.Sprintf("%032x", n+1)
				other.Intent.Target.RequestID = fmt.Sprintf("unresolved-%d", n)
				raw, err := canonical(other)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(`INSERT INTO admissions(id,admission,phase,controller) VALUES(?,?,'prepared','')`, other.ID, string(raw)); err != nil {
					t.Fatal(err)
				}
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			fresh := a
			fresh.ID = strings.Repeat("f", 32)
			fresh.Intent.Target.RequestID = "new-at-capacity"
			if err := j.Reserve(ctx, fresh); !errors.Is(err, ErrAuthority) {
				t.Fatal("unresolved repair budget was bypassed", err)
			}
			if cutoff == "completed" {
				if err := j.Start(ctx, a, controller); err != nil {
					t.Fatal(err)
				}
				if err := j.Reserve(ctx, fresh); !errors.Is(err, ErrAuthority) {
					t.Fatal("starting repair freed its active slot", err)
				}
				if err := j.Complete(ctx, a, controller); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := j.RequestCancel(ctx, a); err != nil {
					t.Fatal("known cancellation refused at active capacity", err)
				}
				if err := j.Reserve(ctx, fresh); !errors.Is(err, ErrAuthority) {
					t.Fatal("cancellation request freed an unproven slot", err)
				}
				if err := j.ConfirmCancelled(ctx, a); err != nil {
					t.Fatal(err)
				}
			}
			if err := j.Reserve(ctx, fresh); err != nil {
				t.Fatal("committed cutoff did not release one active slot", err)
			}
			another := fresh
			another.ID = strings.Repeat("e", 32)
			another.Intent.Target.RequestID = "one-too-many"
			if err := j.Reserve(ctx, another); !errors.Is(err, ErrAuthority) {
				t.Fatal("one cutoff released more than one slot", err)
			}
		})
	}
}
