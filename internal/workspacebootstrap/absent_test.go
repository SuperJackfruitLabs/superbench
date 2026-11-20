package workspacebootstrap

import (
	"errors"
	"reflect"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
)

// Root is asked whether it holds nothing of an unfinished create with the
// very Spec its create sent, even after its owner was stopped. A record with
// a project is refused before root is asked, and root's refusal stands.
func TestAbsentAsksRootWithTheCreatesOwnSpec(t *testing.T) {
	data, b := durableFixture()
	want, err := Specification(data, b)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes, _ := want.Bytes()
	asked := 0
	ask := func(s payloadbootstrap.Spec, input []byte) error {
		asked++
		if !reflect.DeepEqual(s, want) || string(input) != string(wantBytes) {
			t.Fatalf("root asked with another spec: %+v", s)
		}
		return nil
	}
	// The live shape: cancellation stopped the initialization owner.
	stopped := *b
	lease := *b.Lease
	lease.StopRequested, lease.Continuation = true, ""
	stopped.Lease = &lease
	if !workspacepolicy.Unfinished(&stopped) {
		t.Fatal("a stopped materializing record is not unfinished")
	}
	if err = absent(data, &stopped, ask); err != nil || asked != 1 {
		t.Fatalf("unfinished: %v %d", err, asked)
	}
	if err = absent(data, &stopped, func(payloadbootstrap.Spec, []byte) error { return errors.New("root: intent exists") }); err == nil {
		t.Fatal("root's refusal was not returned")
	}
	allocated := stopped
	w := *b.Workspace
	w.Phase = "initializing"
	allocated.Workspace, allocated.ProjectID, allocated.QuotaBytes, allocated.QuotaInodes = &w, 10004, 8<<20, 4096
	if !workspacepolicy.Stalled(&allocated) {
		t.Fatal("the stalled case is not stalled")
	}
	asked = 0
	if err = absent(data, &allocated, ask); !errors.Is(err, ErrBootstrap) || asked != 0 {
		t.Fatalf("a create with a project reached root: %v %d", err, asked)
	}
}
