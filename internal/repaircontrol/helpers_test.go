package repaircontrol

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

func helper(a Admission) operationcontrol.Helper {
	h := operationcontrol.Helper{Command: "/fixed/helper", Policy: "/fixed/policy", State: a.Control.State, StateDevice: 1, StateInode: 2, Kind: "bench", Key: a.Intent.Key, Requested: 8192}
	h.Identity.OperationID, h.Identity.Epoch, h.Identity.StateIdentity, h.Identity.Authority, h.Identity.BinarySHA256 = a.Intent.Target.ID(), a.Intent.Epoch, h.StateIdentity(), strings.Repeat("a", 64), strings.Repeat("b", 64)
	return h
}

func TestExistingCancellationWorksWhenAdmissionBudgetIsFull(t *testing.T) {
	j, a := fixture(t)
	ctx := context.Background()
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
		other.Intent.Target.RequestID = fmt.Sprintf("retained-%d", n)
		raw, err := canonical(other)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(`INSERT INTO admissions(id,admission,phase,controller) VALUES(?,?,'prepared','')`, other.ID, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	newer := a
	newer.ID = strings.Repeat("f", 32)
	if err := j.Reserve(ctx, newer); err == nil {
		t.Fatal("new admission exceeded retained budget")
	}
	if err := j.RequestCancel(ctx, a); err != nil {
		t.Fatal("existing cancellation refused at capacity", err)
	}
	if err := j.Barrier(ctx, a); err != nil {
		t.Fatal(err)
	}
}

func TestIgnoredPreparedConsumptionAndChangedAuthorityCannotAdmitEffects(t *testing.T) {
	for _, kind := range []string{"ignore-reserve", "ignore-start", "prepared-helper", "instance", "unknown-table", "unknown-column"} {
		t.Run(kind, func(t *testing.T) {
			j, a := fixture(t)
			ctx := context.Background()
			controller := strings.Repeat("8", 32)
			if kind == "ignore-reserve" {
				if _, err := j.db.Exec(`CREATE TRIGGER ignore_reserve BEFORE INSERT ON admissions BEGIN SELECT RAISE(IGNORE); END`); err != nil {
					t.Fatal(err)
				}
				if err := j.Reserve(ctx, a); err == nil {
					t.Fatal("ignored reservation accepted")
				}
				return
			}
			if err := j.Reserve(ctx, a); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "ignore-start":
				_, _ = j.db.Exec(`CREATE TRIGGER ignore_start BEFORE UPDATE ON admissions BEGIN SELECT RAISE(IGNORE); END`)
			case "prepared-helper":
				raw, _ := canonical(helper(a))
				_, _ = j.db.Exec(`INSERT INTO helpers(admission_id,kind,key,invocation) VALUES(?,'bench','card',?)`, a.ID, string(raw))
			case "instance":
				_, _ = j.db.Exec(`UPDATE authority SET instance=?`, strings.Repeat("f", 32))
			case "unknown-table":
				_, _ = j.db.Exec(`CREATE TABLE future_authority(id TEXT)`)
			case "unknown-column":
				_, _ = j.db.Exec(`ALTER TABLE admissions ADD COLUMN future_authority TEXT`)
			}
			if err := j.Start(ctx, a, controller); err == nil {
				t.Fatal("changed/ignored admission started")
			}
			if _, err := j.Scope(ctx, a, controller); err == nil {
				t.Fatal("changed/ignored admission granted scope")
			}
		})
	}
}
func TestHelperRecordedBeforeLaunchAndIgnoredInsertCannotAuthorize(t *testing.T) {
	for _, ignored := range []bool{false, true} {
		t.Run(map[bool]string{false: "durable", true: "ignored"}[ignored], func(t *testing.T) {
			j, a := fixture(t)
			ctx := context.Background()
			controller := strings.Repeat("8", 32)
			if err := j.Reserve(ctx, a); err != nil {
				t.Fatal(err)
			}
			if err := j.Start(ctx, a, controller); err != nil {
				t.Fatal(err)
			}
			if ignored {
				if _, err := j.db.Exec(`CREATE TRIGGER ignore_helper BEFORE INSERT ON helpers BEGIN SELECT RAISE(IGNORE); END`); err != nil {
					t.Fatal(err)
				}
			}
			scoped, err := j.Scope(ctx, a, controller)
			if err != nil {
				t.Fatal(err)
			}
			h := helper(a)
			err = operationcontrol.RecordHelper(scoped, h)
			if ignored {
				if err == nil {
					t.Fatal("ignored helper intent authorized launch")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			other, err := OpenExisting(a.Control.State, a.Instance)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			inventory, err := other.Helpers(ctx, a)
			if err != nil || len(inventory) != 1 || inventory[0] != h {
				t.Fatal("helper not durable before launch", inventory, err)
			}
			changed := h
			changed.Identity.BinarySHA256 = strings.Repeat("c", 64)
			if err := operationcontrol.RecordHelper(scoped, changed); err == nil {
				t.Fatal("helper authority replaced")
			}
			if err := other.RequestCancel(ctx, a); err != nil {
				t.Fatal(err)
			}
			if err := operationcontrol.RecordHelper(scoped, h); err == nil {
				t.Fatal("helper admitted after barrier")
			}
			foreign := a
			foreign.Intent.Epoch = strings.Repeat("d", 32)
			if _, err := other.Helpers(ctx, foreign); err == nil {
				t.Fatal("different operation inherited helper proof")
			}
		})
	}
}
