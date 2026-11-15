package workspacerepair_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/output"
)

func hooks(int) string {
	return "[setup]\nonce = 'printf once >> .once-output'\neach = 'printf each >> .each-output'\n"
}
func (f *fixture) count(member int, name string) int {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.initial.Workspace.Members[member].Path, "."+name+"-output"))
	if os.IsNotExist(err) {
		return 0
	}
	must(f.t, err)
	return bytes.Count(raw, []byte(name))
}

func TestRepairPreservesPartialPreparationAndSalvagesKnownSuccessfulStage(t *testing.T) {
	for _, salvage := range []bool{false, true} {
		t.Run(map[bool]string{false: "preserve", true: "salvage"}[salvage], func(t *testing.T) {
			f := newFixture(t, hooks)
			seal := f.m.Prep.Seal
			if salvage {
				var once sync.Once
				f.m.Prep.Seal = func(id string, c *output.Capture, exit *int, truncated bool) error {
					if err := seal(id, c, exit, truncated); err != nil {
						return err
					}
					var stopErr error
					once.Do(func() { _, stopErr = f.m.Prep.Stop("card", f.initial.Lease.Token, false) })
					return stopErr
				}
			}
			_, err := f.m.Prep.Prepare(context.Background(), "card", f.initial.Lease.Token, "a", false)
			if salvage && err == nil {
				t.Fatal("administrative stop did not withhold setup success")
			}
			if !salvage {
				must(t, err)
				f.stop()
			}
			f.m.Prep.Seal = seal
			p := f.inspect()
			want := "preserve"
			if salvage {
				want = "salvage"
			}
			if p.Members[0].Action != want || p.Members[1].Action != "prepare" {
				t.Fatal("wrong partial repair decisions", p.Members)
			}
			owner := f.claim(p, "repair1")
			if f.count(0, "once") != 1 || f.count(0, "each") != 1 {
				t.Fatal("claim repeated known effects")
			}
			out, err := f.m.Continue(context.Background(), "card", "repair1", owner.GrantToken)
			must(t, err)
			if out.Phase != "ready" || f.count(0, "once") != 1 || f.count(0, "each") != 1 || f.count(1, "once") != 1 || f.count(1, "each") != 1 {
				t.Fatal("repair reran preserved once/each or omitted later member")
			}
		})
	}
}

func TestUnknownOnceApprovalSurvivesStopBeforeReplacementStage(t *testing.T) {
	f := newFixture(t, hooks)
	seal := f.m.Prep.Seal
	f.m.Prep.Seal = func(string, *output.Capture, *int, bool) error {
		return errors.New("injected supervisor seal interruption")
	}
	if _, err := f.m.Prep.Prepare(context.Background(), "card", f.initial.Lease.Token, "a", false); err == nil {
		t.Fatal("missing log seal unexpectedly succeeded")
	}
	f.m.Prep.Seal = seal
	f.stop()
	hp, err := f.m.Prep.InspectHistory("card")
	must(t, err)
	_, err = f.m.Prep.ReconcileHistory(*hp)
	must(t, err)
	if _, err := f.m.Inspect(context.Background(), "card", nil); err == nil {
		t.Fatal("unknown once outcome inferred safe automatic rerun")
	}
	p := f.inspect("a")
	if p.Members[0].Action != "rerun-once" {
		t.Fatal("missing explicit once decision")
	}
	first := f.claim(p, "repair1")
	if f.count(0, "once") != 1 {
		t.Fatal("claim itself reran once")
	}
	f.stop()
	if _, err := f.m.Continue(context.Background(), "card", "repair1", first.GrantToken); err == nil {
		t.Fatal("stop before first replacement stage was ignored")
	}
	if _, err := f.m.Inspect(context.Background(), "card", nil); err == nil {
		t.Fatal("second repair forgot uncertainty when Stage became nil")
	}
	p = f.inspect("a")
	if p.PreviousID != "repair1" {
		t.Fatal("repair chain lost prior approval")
	}
	second := f.claim(p, "repair2")
	out, err := f.m.Continue(context.Background(), "card", "repair2", second.GrantToken)
	must(t, err)
	if out.Phase != "ready" || f.count(0, "once") != 2 || f.count(1, "once") != 1 {
		t.Fatal("explicit repeated repair omitted or duplicated approved once effects")
	}
	if len(f.row().Workspace.InitializationRepair.RetiredRuns) != 1 {
		t.Fatal("unknown original run was lost instead of retired")
	}
}
