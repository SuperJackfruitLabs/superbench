package stageexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/output"
)

func TestOpaqueResultRequiresStoppedSealedSuccess(t *testing.T) {
	refusal := errors.New("retained lifecycle failure")
	for _, kind := range []string{"success", "command", "stop", "drain", "seal", "cancel", "overflow"} {
		t.Run(kind, func(t *testing.T) {
			p := plan(t, "printf opaque-result; printf diagnostic >&2")
			p.ResultLimitBytes = 128
			if kind == "overflow" {
				p.ResultLimitBytes = 3
			}
			if kind == "command" {
				p.Args[len(p.Args)-1] += "; exit 7"
			}
			var trace []string
			held := false
			r := transportRunner(t, p, &trace, &held, nil, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "stop" {
				r.Stop = func() (*execution.Execution, error) { trace = append(trace, "stop"); return nil, refusal }
			}
			if kind == "drain" {
				r = transportRunner(t, p, &trace, &held, nil, refusal)
			}
			r.Seal = func(*output.Capture, *int, bool) error {
				trace = append(trace, "seal")
				raw, err := os.ReadFile(p.Execution.Log)
				if err != nil || string(raw) != "diagnostic" {
					t.Fatalf("result mixed into log before seal: %q %v", raw, err)
				}
				if kind == "seal" {
					return refusal
				}
				if kind == "cancel" {
					cancel()
				}
				return nil
			}
			result, err := r.Run(ctx, p)
			if kind == "success" {
				if err != nil || result == nil || string(result.Output) != "opaque-result" || !reflect.DeepEqual(trace, []string{"check", "start", "io", "leader", "stop", "drain", "fence", "seal"}) {
					t.Fatalf("result before successful lifecycle: %+v %v %v", result, err, trace)
				}
				return
			}
			if err == nil || result == nil || result.Output != nil {
				t.Fatalf("uncertain result published: %+v %v", result, err)
			}
			switch kind {
			case "stop", "drain", "seal":
				if !errors.Is(err, refusal) {
					t.Fatalf("lost lifecycle refusal: %v", err)
				}
			case "cancel":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("lost cancellation: %v", err)
				}
			case "overflow":
				if !errors.Is(err, ErrResultOverflow) {
					t.Fatalf("lost overflow: %v", err)
				}
			case "command":
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 7 {
					t.Fatalf("lost child failure: %v", err)
				}
			}
		})
	}
}

func TestOversizedResultLimitRefusesBeforeCaptureAndLaunch(t *testing.T) {
	p := plan(t, "exit 0")
	p.ResultLimitBytes = MaxResultBytes + 1
	r := defaults(p)
	r.Open = func() (*output.Capture, error) { t.Fatal("invalid result limit created log"); return nil, nil }
	r.Start = func(context.Context, *exec.Cmd, *execution.Execution) error {
		t.Fatal("invalid result limit launched")
		return nil
	}
	result, err := r.Run(context.Background(), p)
	if result != nil || !errors.Is(err, ErrResultLimit) {
		t.Fatalf("wrong limit refusal: %+v %v", result, err)
	}
}

func TestOpaqueResultRefusesLegacyWaitBeforeEffects(t *testing.T) {
	p := plan(t, "exit 0")
	p.ResultLimitBytes = 128
	r := defaults(p)
	r.Open = func() (*output.Capture, error) { t.Fatal("legacy result capture created log"); return nil, nil }
	r.Start = func(context.Context, *exec.Cmd, *execution.Execution) error {
		t.Fatal("legacy result capture launched")
		return nil
	}
	result, err := r.Run(context.Background(), p)
	if result != nil || !errors.Is(err, ErrResultTransport) {
		t.Fatalf("wrong transport refusal: %+v %v", result, err)
	}
}
