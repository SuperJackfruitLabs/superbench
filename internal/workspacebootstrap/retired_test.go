package workspacebootstrap

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
)

// Root is asked about an unfinished create with the read-only inspection, and
// about a stalled one with its reclaim, each with the very Spec its create
// sent. Any other record is refused before root is asked, and a reply that
// retired another project than the record's is refused.
func TestRetiredAsksRootWithTheCreatesOwnSpec(t *testing.T) {
	data, b := durableFixture()
	want, err := Specification(data, b)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes, _ := want.Bytes()
	var asked []string
	reply := quotahelper.Assignment{ProjectID: 10004}
	ask := func(name string) func(payloadbootstrap.Spec, []byte) (quotahelper.Assignment, error) {
		return func(s payloadbootstrap.Spec, input []byte) (quotahelper.Assignment, error) {
			asked = append(asked, name)
			if !reflect.DeepEqual(s, want) || string(input) != string(wantBytes) {
				t.Fatalf("root asked with another spec: %+v", s)
			}
			return reply, nil
		}
	}
	inspect, reclaim := ask("inspect"), ask("reclaim")
	if err = retired(data, b, inspect, reclaim); err != nil || !reflect.DeepEqual(asked, []string{"inspect"}) {
		t.Fatalf("unfinished: %v %v", err, asked)
	}
	// Stalled: the bootstrap completed, initialization stopped by the daemon.
	stalled := *b
	w := *b.Workspace
	w.Phase = "initializing"
	lease := *b.Lease
	lease.StopRequested, lease.Continuation = true, ""
	stalled.Workspace, stalled.Lease, stalled.ProjectID, stalled.QuotaBytes, stalled.QuotaInodes = &w, &lease, 10004, 8<<20, 4096
	if !workspacepolicy.Stalled(&stalled) {
		t.Fatal("the stalled case is not stalled")
	}
	asked = nil
	if err = retired(data, &stalled, inspect, reclaim); err != nil || !reflect.DeepEqual(asked, []string{"reclaim"}) {
		t.Fatalf("stalled: %v %v", err, asked)
	}
	stalled.ProjectID = 10005
	if err = retired(data, &stalled, inspect, reclaim); !errors.Is(err, ErrBootstrap) {
		t.Fatalf("another project's retirement accepted: %v", err)
	}
	// An initializing record whose owner was not stopped is neither: root is
	// never asked.
	live := stalled
	liveLease := *b.Lease
	live.Lease, live.ProjectID = &liveLease, 10004
	running := stalled
	runningWorkspace := w
	runningWorkspace.Stage = &model.WorkspaceStage{Name: "api", Digest: strings.Repeat("7", 64)}
	runningLease := lease
	runningLease.Execution = &model.Execution{ID: strings.Repeat("2", 32), Root: "/sys/fs/cgroup/workspace", BootID: "test-boot", HostID: "test-host", Phase: "running", PID: 4242, Log: "/srv/state/run.log", LogLimitBytes: 1 << 20}
	running.Workspace, running.Lease, running.ProjectID = &runningWorkspace, &runningLease, 10004
	asked = nil
	for name, r := range map[string]*model.Bench{"live owner": &live, "a run": &running} {
		if err = workspacepolicy.Validate(r); err != nil {
			t.Fatalf("%s: the case is not a valid record: %v", name, err)
		}
		if err = retired(data, r, inspect, reclaim); !errors.Is(err, ErrBootstrap) || len(asked) != 0 {
			t.Fatalf("%s reached root: %v %v", name, err, asked)
		}
	}
	if err = retired(data, b, func(payloadbootstrap.Spec, []byte) (quotahelper.Assignment, error) {
		return reply, errors.New("root: not retired")
	}, reclaim); err == nil {
		t.Fatal("root's refusal ignored")
	}
	t.Log("STUCK_CREATE_DAEMON_ASKS_ROOT_BY_SHAPE_PASS")
}
