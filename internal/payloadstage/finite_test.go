package payloadstage

import (
	"errors"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/finiteio"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/serviceidentity"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/stageexec"
)

type plan struct {
	limit uint64
	check error
}

func (p plan) Check() error  { return p.check }
func (p plan) Limit() uint64 { return p.limit }
func (p plan) Transport(*model.Bench, string) (stageexec.PrepareTransport, error) {
	return nil, errors.New("not launched")
}
func (p plan) Observe(b []byte) (finiteio.Output, error) {
	return finiteio.Observe(b, finiteio.Input{Version: finiteio.Version, StdoutLimitBytes: p.limit})
}

func TestInstalledFiniteRunRequiresActualInstalledAuthority(t *testing.T) {
	valid := finiteio.Input{Version: finiteio.Version, StdinBase64: "AAEC", StdoutLimitBytes: 1}
	if f, err := NewInstalled(finiteio.Input{Version: "unknown", StdoutLimitBytes: 1}); f != nil || !errors.Is(err, finiteio.ErrInvalid) {
		t.Fatal("invalid input reached authority resolution", err)
	}
	if f, err := NewInstalled(valid); f != nil || !errors.Is(err, serviceidentity.ErrAuthority) {
		t.Fatal("uninstalled host gained a finite plan", err)
	}
	if f, err := newInstalled(serviceidentity.Authority{}, valid, []byte{0, 1, 2}); f != nil || !errors.Is(err, serviceidentity.ErrAuthority) {
		t.Fatal("zero authority gained a finite plan", err)
	}
	var missing *Installed
	if err := missing.Check(); !errors.Is(err, serviceidentity.ErrAuthority) {
		t.Fatal("nil plan passed its gate", err)
	}
	if prepare, err := Transport(nil, nil, ""); prepare != nil || err != nil || Limit(nil) != 0 || Check(nil) != nil {
		t.Fatal("absent plan changed the legacy runner", err)
	}
}

func TestCompletePublishesOnlyItsOwnStoppedSealedExecution(t *testing.T) {
	id := strings.Repeat("a", 32)
	valid := func() (*execution.Execution, *stageexec.Result, *model.Bench) {
		stopped := &execution.Execution{ID: id, Phase: "stopped"}
		current := *stopped
		return &execution.Execution{ID: id, Phase: "starting"}, &stageexec.Result{Execution: stopped, ExitCode: 7, Output: []byte{}}, &model.Bench{Lease: &model.Lease{Execution: &current}}
	}
	launched, out, current := valid()
	observed, err := Complete(plan{limit: 1}, launched, out, current)
	if err != nil || observed == nil || observed.StdoutBytes != 0 || observed.StdoutSHA256 != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatal("exact empty observation refused", observed, err)
	}
	refusal := errors.New("installed identity changed")
	for name, damage := range map[string]func(*plan, *stageexec.Result, *model.Bench){
		"incomplete":      func(_ *plan, o *stageexec.Result, _ *model.Bench) { o.Output = nil },
		"unknown-exit":    func(_ *plan, o *stageexec.Result, _ *model.Bench) { o.ExitCode = -1 },
		"other-execution": func(_ *plan, o *stageexec.Result, _ *model.Bench) { o.Execution.ID = strings.Repeat("b", 32) },
		"not-stopped":     func(_ *plan, o *stageexec.Result, _ *model.Bench) { o.Execution.Phase = "running" },
		"owner-released":  func(_ *plan, _ *stageexec.Result, b *model.Bench) { b.Lease = nil },
		"owner-replaced":  func(_ *plan, _ *stageexec.Result, b *model.Bench) { b.Lease.Execution.ID = strings.Repeat("c", 32) },
		"owner-running":   func(_ *plan, _ *stageexec.Result, b *model.Bench) { b.Lease.Execution.Phase = "running" },
		"identity":        func(p *plan, _ *stageexec.Result, _ *model.Bench) { p.check = refusal },
		"over-limit":      func(_ *plan, o *stageexec.Result, _ *model.Bench) { o.Output = []byte{1, 2} },
	} {
		p := plan{limit: 1}
		launched, out, current := valid()
		damage(&p, out, current)
		if observed, err := Complete(p, launched, out, current); observed != nil || err == nil {
			t.Fatal("finite observation published after", name)
		}
	}
	if observed, err := Complete(nil, launched, out, current); observed != nil || !errors.Is(err, ErrIncomplete) {
		t.Fatal("absent plan published output", err)
	}
}
