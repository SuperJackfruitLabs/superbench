package stageexec

import (
	"context"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/output"
	"os/exec"
	"reflect"
	"testing"
)

type testTransport struct {
	cmd                *exec.Cmd
	trace              *[]string
	held               *bool
	checkErr, drainErr error
}

func (t *testTransport) Check() error {
	if !*t.held {
		return errors.New("transport checked outside launch gate")
	}
	*t.trace = append(*t.trace, "check")
	return t.checkErr
}
func (t *testTransport) StartIO() error {
	if !*t.held || t.cmd.Process == nil {
		return errors.New("frame delivered before atomic start")
	}
	*t.trace = append(*t.trace, "io")
	return nil
}
func (t *testTransport) WaitLeader() error {
	*t.trace = append(*t.trace, "leader")
	return t.cmd.Wait()
}
func (t *testTransport) Failures() <-chan error { return nil }
func (t *testTransport) Drain(_ context.Context, f interface {
	Fence(*execution.Execution) error
}) error {
	*t.trace = append(*t.trace, "drain")
	if err := f.Fence(nil); err != nil {
		return err
	}
	return t.drainErr
}
func (t *testTransport) Close() error { return nil }

func transportRunner(t *testing.T, p Plan, trace *[]string, held *bool, checkErr, drainErr error) Runner {
	t.Helper()
	r := defaults(p)
	r.Start = nil
	r.Prepare = func(cmd *exec.Cmd, _ *execution.Execution) (Transport, error) {
		return &testTransport{cmd: cmd, trace: trace, held: held, checkErr: checkErr, drainErr: drainErr}, nil
	}
	r.StartPrepared = func(_ context.Context, cmd *exec.Cmd, _ *execution.Execution, launch Launch) error {
		*held = true
		defer func() { *held = false }()
		return launch(func() error { *trace = append(*trace, "start"); return cmd.Start() })
	}
	r.Stop = func() (*execution.Execution, error) { *trace = append(*trace, "stop"); return stopped(p), nil }
	r.Backend = fenceFunc(func(*execution.Execution) error { *trace = append(*trace, "fence"); return nil })
	r.Cancel = func() { *trace = append(*trace, "cancel") }
	r.Seal = func(*output.Capture, *int, bool) error { *trace = append(*trace, "seal"); return nil }
	return r
}

func TestPreparedTransportStartsInsideGateAndDrainsOnlyAfterLeaderAndStop(t *testing.T) {
	p := plan(t, "printf retained-output")
	trace := []string{}
	held := false
	r := transportRunner(t, p, &trace, &held, nil, nil)
	if _, err := r.Run(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	want := []string{"check", "start", "io", "leader", "stop", "drain", "fence", "seal"}
	if !reflect.DeepEqual(trace, want) {
		t.Fatalf("unsafe transport lifecycle: %v", trace)
	}
}

func TestChangedTransportAuthorityRefusesActualStart(t *testing.T) {
	p := plan(t, "exit 0")
	trace := []string{}
	held := false
	changed := errors.New("installed authority changed")
	r := transportRunner(t, p, &trace, &held, changed, nil)
	if _, err := r.Run(context.Background(), p); !errors.Is(err, changed) {
		t.Fatalf("wanted precise authority refusal: %v", err)
	}
	if !reflect.DeepEqual(trace, []string{"check"}) {
		t.Fatalf("effects after authority refusal: %v", trace)
	}
}

func TestPumpFailureRevokesStoppedOwnerAndCannotSeal(t *testing.T) {
	p := plan(t, "printf partial-output")
	trace := []string{}
	held := false
	failed := errors.New("actual transport pump failed")
	r := transportRunner(t, p, &trace, &held, nil, failed)
	if _, err := r.Run(context.Background(), p); !errors.Is(err, failed) {
		t.Fatalf("wanted precise IO failure: %v", err)
	}
	want := []string{"check", "start", "io", "leader", "stop", "drain", "fence", "cancel"}
	if !reflect.DeepEqual(trace, want) {
		t.Fatalf("failed transport sealed or failed to revoke: %v", trace)
	}
}

func TestPreparedTransportCannotUseLegacyUngatedStart(t *testing.T) {
	p := plan(t, "exit 0")
	r := defaults(p)
	prepared := false
	r.Prepare = func(*exec.Cmd, *execution.Execution) (Transport, error) { prepared = true; return nil, nil }
	if _, err := r.Run(context.Background(), p); err == nil || prepared {
		t.Fatalf("ungated transport admitted: %v %v", prepared, err)
	}
}

func TestAmbiguousStartCallbacksRefuseBeforeCaptureOrPreparation(t *testing.T) {
	p := plan(t, "exit 0")
	r := defaults(p)
	effects := false
	r.StartPrepared = func(context.Context, *exec.Cmd, *execution.Execution, Launch) error { effects = true; return nil }
	r.Prepare = func(*exec.Cmd, *execution.Execution) (Transport, error) { effects = true; return nil, nil }
	r.Open = func() (*output.Capture, error) { effects = true; return nil, nil }
	if result, err := r.Run(context.Background(), p); result != nil || !errors.Is(err, ErrAmbiguousStart) || effects {
		t.Fatalf("ambiguous callbacks admitted effects: %v %v %v", result, err, effects)
	}
}
