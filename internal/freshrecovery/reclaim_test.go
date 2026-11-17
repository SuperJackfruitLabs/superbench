package freshrecovery

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/cleanworker"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
)

// world is one bench as a reclaim sees it, with a fault that fires before or
// after one named effect: a refusal, or a crash or lost reply after it.
type world struct {
	phase                  string
	present, full, retired bool
	fenceErr               error
	busy                   []bool // idle results in call order, then idle
	calls                  []string
	faultStep, faultWhen   string
}

var errFault = errors.New("injected fault")

func (w *world) effect(name string, ok func() error, apply func()) func() error {
	return func() error {
		w.calls = append(w.calls, name)
		if w.faultStep == name && w.faultWhen == "before" {
			w.faultStep = ""
			return errFault
		}
		if err := ok(); err != nil {
			return err
		}
		apply()
		if w.faultStep == name && w.faultWhen == "after" {
			w.faultStep = ""
			return errFault
		}
		return nil
	}
}

func (w *world) steps() reclaimSteps {
	return reclaimSteps{
		phase:  func() (string, error) { return w.phase, nil },
		leaf:   func() (bool, error) { w.calls = append(w.calls, "leaf"); return w.present, nil },
		fenced: func() error { w.calls = append(w.calls, "fenced"); return w.fenceErr },
		idle: func() error {
			w.calls = append(w.calls, "idle")
			if len(w.busy) != 0 {
				busy := w.busy[0]
				w.busy = w.busy[1:]
				if busy {
					return ErrReclaimBusy
				}
			}
			return nil
		},
		retain: w.effect("retain", func() error {
			if w.phase != "completed" && w.phase != "reclaiming" {
				return errors.New("retain from " + w.phase)
			}
			return nil
		}, func() { w.phase = "reclaiming" }),
		purge: w.effect("purge", func() error {
			if w.phase != "reclaiming" {
				return errors.New("purge before retention")
			}
			return nil
		}, func() { w.full = false }),
		remove: w.effect("remove", func() error {
			if w.full {
				return errors.New("leaf not empty")
			}
			return nil
		}, func() { w.present = false }),
		retire: w.effect("retire", func() error {
			if w.present {
				return errors.New("leaf present")
			}
			return nil
		}, func() { w.retired = true }),
		finish: w.effect("finish", func() error {
			if !w.retired {
				return errors.New("finish before tombstone")
			}
			return nil
		}, func() { w.phase = "reclaimed" }),
	}
}

func released() *world { return &world{phase: "completed", present: true, full: true} }

func TestReclaimRunsItsEffectsInTheirOnlyOrder(t *testing.T) {
	w := released()
	retained, err := w.steps().run()
	if err != nil || !retained {
		t.Fatalf("reclaim: %v %v", retained, err)
	}
	want := []string{"leaf", "fenced", "idle", "retain", "leaf", "idle", "purge", "remove", "retire", "finish"}
	if !reflect.DeepEqual(w.calls, want) || w.phase != "reclaimed" || w.present || !w.retired {
		t.Fatalf("calls %v, state %+v", w.calls, w)
	}
	// A lost reply: the exact retry proves the same tombstone and writes nothing.
	w.calls = nil
	retained, err = w.steps().run()
	if err != nil || !retained || !reflect.DeepEqual(w.calls, []string{"leaf", "retire"}) {
		t.Fatalf("retry after reclaimed: %v %v %v", retained, err, w.calls)
	}
}

func TestReclaimRefusesBeforeRetainingAnything(t *testing.T) {
	for name, w := range map[string]*world{
		"leaf-gone":    {phase: "completed"},
		"unfenced":     {phase: "completed", present: true, full: true, fenceErr: errors.New("git controls open")},
		"staged":       {phase: "staged", present: true, full: true},
		"transferring": {phase: "transferring", present: true, full: true},
		"reserved":     {phase: "reserved"},
	} {
		retained, err := w.steps().run()
		if err == nil || retained || w.phase == "reclaiming" || w.retired {
			t.Errorf("%s: retained %v, %v, state %+v", name, retained, err, w)
		}
		for _, call := range w.calls {
			if call != "leaf" && call != "fenced" && call != "idle" {
				t.Errorf("%s ran %s before refusing", name, call)
			}
		}
	}
	if _, err := (&world{phase: "completed"}).steps().run(); !errors.Is(err, errLeafGone) {
		t.Fatalf("a missing leaf: %v", err)
	}
	// Something at the leaf's name after the reclaim finished is not ours.
	w := &world{phase: "reclaimed", present: true, retired: true}
	if retained, err := w.steps().run(); !errors.Is(err, ErrRecovery) || !retained || w.retired && len(w.calls) != 1 {
		t.Fatalf("reclaimed with a leaf: %v %v %v", retained, err, w.calls)
	}
}

// A crash or refusal before or after each effect leaves a state the same
// reclaim then finishes, and every error after retention says it may follow
// effects.
func TestReclaimRetriesAfterAFaultAtEachStep(t *testing.T) {
	for _, step := range []string{"retain", "purge", "remove", "retire", "finish"} {
		for _, when := range []string{"before", "after"} {
			t.Run(fmt.Sprintf("%s-%s", when, step), func(t *testing.T) {
				w := released()
				w.faultStep, w.faultWhen = step, when
				retained, err := w.steps().run()
				if !errors.Is(err, errFault) || !retained {
					t.Fatalf("fault not reported as retained: %v %v", retained, err)
				}
				for attempt := 0; attempt < 2; attempt++ {
					w.calls = nil
					if retained, err = w.steps().run(); err != nil || !retained {
						t.Fatalf("retry %d: %v %v %v", attempt, retained, err, w.calls)
					}
				}
				if w.phase != "reclaimed" || w.present || w.full || !w.retired {
					t.Fatalf("retry left %+v", w)
				}
			})
		}
	}
}

func TestOnlyAPurgeRunsAsThePayloadAccount(t *testing.T) {
	p := payloadpolicy.Policy{DaemonUID: 61001, DaemonGID: 61003, PayloadUID: 61002, PayloadGID: 61004}
	for mode, want := range map[string][2]uint32{cleanworker.Observe: {61001, 61003}, cleanworker.Delete: {61001, 61003}, cleanworker.Purge: {61002, 61004}} {
		uid, gid, err := workerAccount(p, mode)
		if err != nil || [2]uint32{uid, gid} != want {
			t.Errorf("%s runs as %d:%d %v", mode, uid, gid, err)
		}
	}
	for name, change := range map[string]func(*payloadpolicy.Policy){
		"payload-is-daemon": func(p *payloadpolicy.Policy) { p.PayloadUID = p.DaemonUID },
		"payload-group":     func(p *payloadpolicy.Policy) { p.PayloadGID = p.DaemonGID },
		"no-payload":        func(p *payloadpolicy.Policy) { p.PayloadUID = 0 },
		"no-payload-group":  func(p *payloadpolicy.Policy) { p.PayloadGID = 0 },
	} {
		q := p
		change(&q)
		if _, _, err := workerAccount(q, cleanworker.Purge); !errors.Is(err, ErrRecovery) {
			t.Errorf("%s purge admitted", name)
		}
	}
	if _, _, err := workerAccount(payloadpolicy.Policy{PayloadUID: 1, PayloadGID: 1}, cleanworker.Observe); err == nil {
		t.Error("observe without a daemon account")
	}
	if _, _, err := workerAccount(p, "remove"); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("unknown mode: %v", err)
	}
}

// Root's own liveness check refuses before retention and, on a retained
// reclaim, before the purge: a bench in use is never purged.
func TestReclaimRefusesWhileTheBenchIsInUse(t *testing.T) {
	w := released()
	w.busy = []bool{true}
	if retained, err := w.steps().run(); !errors.Is(err, ErrReclaimBusy) || retained || w.phase != "completed" {
		t.Fatalf("busy before retention: %v %v %+v", retained, err, w)
	}
	w = released()
	w.busy = []bool{false, true}
	retained, err := w.steps().run()
	if !errors.Is(err, ErrReclaimBusy) || !retained || !w.full || !w.present {
		t.Fatalf("busy before the purge: %v %v %+v", retained, err, w)
	}
	for _, call := range w.calls {
		if call == "purge" || call == "remove" || call == "retire" {
			t.Fatalf("a busy bench ran %s", call)
		}
	}
	w.calls = nil
	if retained, err = w.steps().run(); err != nil || !retained || w.phase != "reclaimed" {
		t.Fatalf("retry once idle: %v %v", retained, err)
	}
}

func TestOnlyAPurgeWorkerIsWatched(t *testing.T) {
	if !watched(cleanworker.Purge) || watched(cleanworker.Observe) || watched(cleanworker.Delete) {
		t.Fatal("watched worker modes")
	}
}
