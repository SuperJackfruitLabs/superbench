package workspaceprep_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceprep"
)

// Stop wins after a starting scope/history intent is durable but before the
// delayed runner opens capture. A reviewed absence must remain absent even when
// that runner resumes. This exercises the public setup and payload paths using
// real SQLite/Git and the fixture's simple-child backend, not cgroup isolation.
func TestStoppedPendingCaptureCannotCreateLogAfterReviewedAbsence(t *testing.T) {
	for _, payload := range []bool{false, true} {
		name := "setup"
		if payload {
			name = "payload"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, 1, nil)
			state, err := filepath.EvalSymlinks(f.store.State)
			if err != nil {
				t.Fatal(err)
			}
			f.store.State, f.manager.State = state, state
			if payload {
				f.ready()
			}
			base := f.manager.Gate
			intercepted := false
			startsAtStop := -1
			var plan *workspaceprep.HistoryPlan
			var observed history.Record
			f.manager.Gate = func(key string, fn func() error) error {
				if err := base(key, fn); err != nil {
					return err
				}
				if intercepted {
					return nil
				}
				current := f.read()
				if current.Lease == nil || current.Lease.Execution == nil || current.Lease.Execution.Phase != "starting" || (current.Workspace.Stage == nil) != payload {
					return nil
				}
				intercepted = true // Nested stop/inspection gates must not intercept again.
				_, startsAtStop = f.backend.counts()
				stopped, err := f.manager.Stop(key, "", true)
				if err != nil {
					return err
				}
				if _, err := os.Lstat(stopped.Lease.Execution.Log); !os.IsNotExist(err) {
					t.Fatal("capture existed before its gated open", err)
				}
				plan, err = f.manager.InspectHistory(key)
				if err != nil {
					return err
				}
				if plan.File != nil || plan.Size != 0 {
					t.Fatal("inspection fabricated a capture artifact")
				}
				raw, err := plan.Bytes()
				if err != nil {
					return err
				}
				if strings.Contains(string(raw), f.initial.Lease.Token) || strings.Contains(string(raw), f.initial.Lease.Continuation) {
					t.Fatal("review plan exposed private ownership")
				}
				observed, err = f.manager.ReconcileHistory(*plan)
				if err != nil {
					return err
				}
				if observed.Interruption == nil || observed.Interruption.LogState != "absent" || observed.File != nil || observed.ExitCode != nil || observed.Truncated {
					t.Fatal("absence approval invented outcome or file evidence")
				}
				return nil // Resume the old runner only after durable reviewed absence.
			}
			var operationErr error
			if payload {
				_, operationErr = f.run("", "/bin/sh", "-c", "printf unauthorized > late-payload")
			} else {
				_, operationErr = f.prepare(context.Background(), "")
			}
			if operationErr == nil || !intercepted || plan == nil {
				t.Fatal("pending runner bypassed stop/capture boundary", operationErr)
			}
			if _, starts := f.backend.counts(); starts != startsAtStop {
				t.Fatal("child launched after reviewed absence")
			}
			if _, err := os.Lstat(plan.Execution.Log); !os.IsNotExist(err) {
				t.Fatal("resumed runner created a late log", err)
			}
			current := f.read()
			if !current.Lease.StopRequested || current.Lease.Continuation != "" || current.Lease.Execution.Phase != "stopped" || current.Lease.Execution.ID != plan.Execution.ID {
				t.Fatal("late runner changed stopped ownership")
			}
			replay, err := f.manager.ReconcileHistory(*plan)
			if err != nil || !reflect.DeepEqual(replay, observed) {
				t.Fatal("late runner changed reviewed history", err)
			}
			if !payload && (len(current.Workspace.Preparation) != 0 || f.count(0, "once-count") != 0) {
				t.Fatal("stopped setup manufactured success/effects")
			}
		})
	}
}
