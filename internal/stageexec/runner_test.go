package stageexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/output"
)

type fenceFunc func(*execution.Execution) error

func (f fenceFunc) Fence(e *execution.Execution) error { return f(e) }
func plan(t *testing.T, script string) Plan {
	t.Helper()
	root := t.TempDir()
	return Plan{Execution: &execution.Execution{ID: strings.Repeat("a", 32), Root: root, BootID: "boot", HostID: "host", Phase: "starting", Log: filepath.Join(root, "run.log"), LogLimitBytes: 4096}, Dir: root, Args: []string{"/bin/sh", "-c", script}, Environment: append(os.Environ(), "STAGE_EXEC_TEST=present")}
}
func stopped(p Plan) *execution.Execution { e := *p.Execution; e.Phase = "stopped"; return &e }
func defaults(p Plan) Runner {
	return Runner{Backend: fenceFunc(func(*execution.Execution) error { return nil }), Start: func(_ context.Context, c *exec.Cmd, _ *execution.Execution) error { return c.Start() }, Stop: func() (*execution.Execution, error) { return stopped(p), nil }, Cancel: func() {}, Seal: func(*output.Capture, *int, bool) error { return nil }}
}

func TestRunStopsAndSealsBeforeReturningCommandOutcome(t *testing.T) {
	for _, exit := range []int{0, 7} {
		t.Run(fmt.Sprint(exit), func(t *testing.T) {
			p := plan(t, fmt.Sprintf("printf 'out:%%s\\n' \"$STAGE_EXEC_TEST\"; printf 'error\\n' >&2; exit %d", exit))
			var trace []string
			r := defaults(p)
			r.Start = func(_ context.Context, c *exec.Cmd, e *execution.Execution) error {
				trace = append(trace, "start")
				if c.Dir != p.Dir || e != p.Execution || !reflect.DeepEqual(c.Env, p.Environment) {
					t.Fatal("payload plan changed")
				}
				return c.Start()
			}
			r.Stop = func() (*execution.Execution, error) { trace = append(trace, "stop"); return stopped(p), nil }
			r.Seal = func(c *output.Capture, code *int, truncated bool) error {
				trace = append(trace, "seal")
				if code == nil || *code != exit || truncated {
					t.Fatalf("wrong sealed outcome: %v %v", code, truncated)
				}
				raw, err := os.ReadFile(p.Execution.Log)
				if err != nil || string(raw) != "out:present\nerror\n" {
					t.Fatalf("capture not complete before sealing: %q %v", raw, err)
				}
				if _, err := c.File.Stat(); err != nil {
					t.Fatalf("capture closed before seal: %v", err)
				}
				return nil
			}
			result, err := r.Run(context.Background(), p)
			if exit == 0 && err != nil || exit != 0 && (err == nil || !strings.Contains(err.Error(), "command failed; stopped lease retained")) {
				t.Fatalf("outcome: %v", err)
			}
			if result == nil || result.ExitCode != exit || result.Execution.Phase != "stopped" || result.LogTruncated {
				t.Fatalf("wrong result: %+v", result)
			}
			if !reflect.DeepEqual(trace, []string{"start", "stop", "seal"}) {
				t.Fatalf("lifecycle ordering: %v", trace)
			}
		})
	}
}

func TestRunTruncatesAndSealsBoundedOutput(t *testing.T) {
	p := plan(t, "i=0; while [ $i -lt 100 ]; do printf '0123456789'; i=$((i + 1)); done")
	p.Execution.LogLimitBytes = 128
	r := defaults(p)
	sealed := false
	r.Seal = func(_ *output.Capture, code *int, truncated bool) error {
		sealed = true
		if *code != 0 || !truncated {
			t.Fatal("truncation not recorded")
		}
		return nil
	}
	result, err := r.Run(context.Background(), p)
	if err != nil || result == nil || !result.LogTruncated || !sealed {
		t.Fatalf("bounded execution: %+v %v", result, err)
	}
	raw, err := os.ReadFile(p.Execution.Log)
	if err != nil || len(raw) > 128 || !strings.HasSuffix(string(raw), output.Marker) {
		t.Fatalf("capture exceeded bound/lost marker: %d %v", len(raw), err)
	}
}

func TestStartFailureRetainsStartingExecutionAndSkipsStopSeal(t *testing.T) {
	for _, launched := range []bool{false, true} {
		for _, fenceFails := range []bool{false, true} {
			t.Run(fmt.Sprintf("launched=%v/fenceFailure=%v", launched, fenceFails), func(t *testing.T) {
				p := plan(t, "exec sleep 30")
				startErr, fenceErr := errors.New("ambiguous launch save"), errors.New("cannot confirm fencing")
				r := defaults(p)
				var cmd *exec.Cmd
				fenced := 0
				r.Start = func(_ context.Context, c *exec.Cmd, _ *execution.Execution) error {
					cmd = c
					if launched {
						if err := c.Start(); err != nil {
							return err
						}
					}
					return startErr
				}
				r.Backend = fenceFunc(func(e *execution.Execution) error {
					fenced++
					if e != p.Execution {
						t.Fatal("fenced different execution")
					}
					// Test owns only this exact child; no process-group isolation is claimed.
					_ = cmd.Process.Kill()
					if fenceFails {
						return fenceErr
					}
					return nil
				})
				r.Stop = func() (*execution.Execution, error) { t.Fatal("ambiguous start marked stopped"); return nil, nil }
				r.Seal = func(*output.Capture, *int, bool) error { t.Fatal("ambiguous start sealed as outcome"); return nil }
				result, err := r.Run(context.Background(), p)
				if !errors.Is(err, startErr) || result == nil || result.Execution != p.Execution || result.ExitCode != -1 {
					t.Fatalf("start failure lost retained intent: %+v %v", result, err)
				}
				if launched && fenced != 1 || !launched && fenced != 0 {
					t.Fatalf("wrong fencing count: %d", fenced)
				}
				if launched && fenceFails && !errors.Is(err, fenceErr) {
					t.Fatalf("fence error lost: %v", err)
				}
			})
		}
	}
}

func TestStopFailureNeverSealsLog(t *testing.T) {
	p := plan(t, "printf 'retained'")
	stopErr := errors.New("writer remains")
	r := defaults(p)
	r.Stop = func() (*execution.Execution, error) { return nil, stopErr }
	r.Seal = func(*output.Capture, *int, bool) error { t.Fatal("unfenced stage sealed"); return nil }
	result, err := r.Run(context.Background(), p)
	if !errors.Is(err, stopErr) || !strings.Contains(err.Error(), "unable to prove execution stopped; lease retained") || result == nil || result.Execution != p.Execution {
		t.Fatalf("stop failure: %+v %v", result, err)
	}
}

func TestSealFailureRetainsStoppedExecutionAndOutcome(t *testing.T) {
	p := plan(t, "exit 0")
	sealErr := errors.New("state save failed")
	r := defaults(p)
	r.Seal = func(*output.Capture, *int, bool) error { return sealErr }
	result, err := r.Run(context.Background(), p)
	if !errors.Is(err, sealErr) || !strings.Contains(err.Error(), "log history save failed; stopped lease retained") || result == nil || result.Execution.Phase != "stopped" || result.ExitCode != 0 {
		t.Fatalf("seal failure: %+v %v", result, err)
	}
}

func TestCancellationFencesBeforeReconciliationCallback(t *testing.T) {
	p := plan(t, "exec sleep 30")
	r := defaults(p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, canceled := make(chan struct{}), make(chan struct{})
	var cmd *exec.Cmd
	var mu sync.Mutex
	fenced := false
	r.Start = func(_ context.Context, c *exec.Cmd, _ *execution.Execution) error {
		cmd = c
		err := c.Start()
		close(started)
		return err
	}
	r.Backend = fenceFunc(func(*execution.Execution) error { mu.Lock(); fenced = true; mu.Unlock(); return cmd.Process.Kill() })
	r.Cancel = func() {
		mu.Lock()
		if !fenced {
			t.Error("cancel callback preceded scope fencing")
		}
		mu.Unlock()
		close(canceled)
	}
	done := make(chan error, 1)
	go func() { _, err := r.Run(ctx, p); done <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("payload did not start")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel callback not invoked")
	}
	select {
	case err := <-done:
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != -1 {
			t.Fatal("canceled payload did not report the killed process outcome", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled payload not reaped")
	}
}

func TestExistingLogAndInvalidPlanHaveNoLaunchEffects(t *testing.T) {
	p := plan(t, "exit 0")
	if err := os.WriteFile(p.Execution.Log, []byte("prior evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	r := defaults(p)
	r.Start = func(context.Context, *exec.Cmd, *execution.Execution) error {
		t.Fatal("existing log launched a payload")
		return nil
	}
	r.Stop = func() (*execution.Execution, error) { t.Fatal("existing log reached Stop"); return nil, nil }
	r.Seal = func(*output.Capture, *int, bool) error { t.Fatal("existing log reached Seal"); return nil }
	if result, err := r.Run(context.Background(), p); !errors.Is(err, os.ErrExist) || result == nil {
		t.Fatalf("existing log adopted: %+v %v", result, err)
	}
	raw, err := os.ReadFile(p.Execution.Log)
	if err != nil || string(raw) != "prior evidence" {
		t.Fatal("existing evidence overwritten")
	}
	for _, change := range []func(*Runner, *Plan){
		func(_ *Runner, p *Plan) { p.Execution = nil },
		func(_ *Runner, p *Plan) { p.Args = nil },
		func(r *Runner, _ *Plan) { r.Backend = nil },
		func(r *Runner, _ *Plan) { r.Start = nil },
		func(r *Runner, _ *Plan) { r.Stop = nil },
		func(r *Runner, _ *Plan) { r.Cancel = nil },
		func(r *Runner, _ *Plan) { r.Seal = nil },
	} {
		next := plan(t, "exit 0")
		runner := defaults(next)
		change(&runner, &next)
		runner.Open = func() (*output.Capture, error) { t.Fatal("invalid plan opened capture"); return nil, nil }
		if result, err := runner.Run(context.Background(), next); err == nil || err.Error() != "execution stage requires a plan, fencing backend and lifecycle callbacks" || result != nil {
			t.Fatalf("invalid stage accepted: %+v %v", result, err)
		}
	}
}
