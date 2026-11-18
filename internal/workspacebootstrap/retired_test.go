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

// Root is asked about an unfinished create with the very Spec its create
// sent; any other record is refused before root is asked, and a reply that
// retired another project than the record's is refused.
func TestRetiredAsksRootWithTheCreatesOwnSpec(t *testing.T) {
	data, b := durableFixture()
	want, err := Specification(data, b)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes, _ := want.Bytes()
	asked := 0
	reply := quotahelper.RetiredResult{Allocation: quotahelper.Assignment{ProjectID: 10004}}
	ask := func(s payloadbootstrap.Spec, input []byte) (quotahelper.RetiredResult, error) {
		asked++
		if !reflect.DeepEqual(s, want) || string(input) != string(wantBytes) {
			t.Fatalf("root asked with another spec: %+v", s)
		}
		return reply, nil
	}
	if err = retired(data, b, ask); err != nil || asked != 1 {
		t.Fatalf("materializing: %v %d", err, asked)
	}
	initializing := *b
	w := *b.Workspace
	w.Phase = "initializing"
	initializing.Workspace, initializing.ProjectID, initializing.QuotaBytes, initializing.QuotaInodes = &w, 10004, 8<<20, 4096
	if err = retired(data, &initializing, ask); err != nil || asked != 2 {
		t.Fatalf("initializing: %v %d", err, asked)
	}
	initializing.ProjectID = 10005
	if err = retired(data, &initializing, ask); !errors.Is(err, ErrBootstrap) {
		t.Fatalf("another project's retirement accepted: %v", err)
	}
	// A record with a run is not unfinished, though it is valid.
	running := initializing
	runningWorkspace := w
	runningWorkspace.Stage = &model.WorkspaceStage{Name: "api", Digest: strings.Repeat("7", 64)}
	lease := *b.Lease
	lease.Execution = &model.Execution{ID: strings.Repeat("2", 32), Root: "/sys/fs/cgroup/workspace", BootID: "test-boot", HostID: "test-host", Phase: "stopped", Log: "/srv/state/run.log", LogLimitBytes: 1 << 20}
	running.Workspace, running.Lease, running.ProjectID = &runningWorkspace, &lease, 10004
	if err = workspacepolicy.Validate(&running); err != nil {
		t.Fatal("the running case is not a valid record:", err)
	}
	if err = retired(data, &running, ask); !errors.Is(err, ErrBootstrap) || asked != 3 {
		t.Fatalf("a record with a run reached root: %v %d", err, asked)
	}
	if err = retired(data, b, func(payloadbootstrap.Spec, []byte) (quotahelper.RetiredResult, error) {
		return reply, errors.New("root: not retired")
	}); err == nil {
		t.Fatal("root's refusal ignored")
	}
}
