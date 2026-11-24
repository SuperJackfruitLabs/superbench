package workspacepolicy_test

import (
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
)

func remote(id, phase string) *model.Execution {
	return &model.Execution{ID: strings.Repeat(id, 32), Root: "/sys/fs/cgroup/superbench", BootID: "test-boot", HostID: "test-host", Phase: phase, Kind: "remote"}
}
func run(id, phase string) *model.Execution {
	return &model.Execution{ID: strings.Repeat(id, 32), Root: "/sys/fs/cgroup/superbench", BootID: "test-boot", HostID: "test-host", Phase: phase, Log: "/srv/state/run-" + strings.Repeat(id, 32) + ".log", LogLimitBytes: 1 << 20}
}

// Every remote execution transition, through the state transition check that
// every update runs. Only grant, replacement and fence move a remote scope.
func TestRemoteExecutionTransitions(t *testing.T) {
	step := func(from, to *model.Execution, change func(*model.Lease)) error {
		old := workspacepolicy.RemoteRecord(t)
		old.Lease.Execution = from
		before := benchstate.Capture(benchstate.Database{"card": old})
		next := workspacepolicy.RemoteRecord(t)
		next.Lease.Execution = to
		if change != nil {
			change(next.Lease)
		}
		return benchstate.ValidateTransition(before, benchstate.Database{"card": next})
	}
	retainRemote := func(l *model.Lease) { l.RetainedRuns = []string{strings.Repeat("7", 32)} }
	for name, err := range map[string]error{
		"grant":                step(nil, remote("7", "running"), nil),
		"keep":                 step(remote("7", "running"), remote("7", "running"), nil),
		"fence-running":        step(remote("7", "running"), nil, nil),
		"fence-stopped":        step(remote("7", "stopped"), nil, nil),
		"stop":                 step(remote("7", "running"), remote("7", "stopped"), nil),
		"replace-running":      step(remote("7", "running"), remote("8", "running"), nil),
		"replace-stopped":      step(remote("7", "stopped"), remote("8", "running"), nil),
		"run-start":            step(nil, run("8", "starting"), nil),
		"run-retained-release": step(run("8", "stopped"), nil, func(l *model.Lease) { l.RetainedRuns = []string{strings.Repeat("8", 32)} }),
	} {
		if err != nil {
			t.Errorf("%s refused: %v", name, err)
		}
	}
	for name, err := range map[string]error{
		"grant-starting":     step(nil, remote("7", "starting"), nil),
		"grant-stopped":      step(nil, remote("7", "stopped"), nil),
		"restart":            step(remote("7", "stopped"), remote("7", "running"), nil),
		"replace-starting":   step(remote("7", "running"), remote("8", "starting"), nil),
		"replace-stopped-to": step(remote("7", "running"), remote("8", "stopped"), nil),
		"remote-to-run":      step(remote("7", "running"), run("8", "starting"), nil),
		"run-to-remote":      step(run("8", "stopped"), remote("7", "running"), func(l *model.Lease) { l.RetainedRuns = []string{strings.Repeat("8", 32)} }),
		"running-run-remote": step(run("8", "running"), remote("7", "running"), nil),
		"run-becomes-remote": step(run("8", "stopped"), func() *model.Execution { e := run("8", "stopped"); e.Kind = "remote"; return e }(), nil),
		"remote-becomes-run": step(remote("7", "running"), func() *model.Execution { e := remote("7", "running"); e.Kind = ""; return e }(), nil),
		"unknown-kind-grant": step(nil, func() *model.Execution { e := remote("7", "running"); e.Kind = "shell"; return e }(), nil),
		"unknown-kind-clear": step(func() *model.Execution { e := remote("7", "running"); e.Kind = "shell"; return e }(), nil, nil),
		"fence-retains":      step(remote("7", "running"), nil, retainRemote),
		"replace-retains":    step(remote("7", "stopped"), remote("8", "running"), retainRemote),
		"moved-root":         step(remote("7", "running"), func() *model.Execution { e := remote("7", "running"); e.Root = "/sys/fs/cgroup/other"; return e }(), nil),
	} {
		if err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestRemoteExecutionTransitionLeavesRunsToTheOrdinaryRules(t *testing.T) {
	for _, pair := range [][2]*model.Execution{{nil, nil}, {nil, run("8", "starting")}, {run("8", "running"), run("8", "stopped")}, {run("8", "stopped"), nil}} {
		if _, decided := workspacepolicy.RemoteExecutionTransition(pair[0], pair[1], nil); decided {
			t.Errorf("remote rule decided a run transition %v -> %v", pair[0], pair[1])
		}
	}
	if _, decided := workspacepolicy.RemoteExecutionTransition(remote("7", "running"), remote("7", "stopped"), nil); decided {
		t.Error("remote rule decided a same-scope phase change the ordinary rules own")
	}
}
