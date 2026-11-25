package model_test

import (
	"regexp"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

var leaseID = regexp.MustCompile(`^[a-f0-9]{32}$`)

func TestNewLeaseHasIdentity(t *testing.T) {
	l, err := model.NewLease("writer")
	if err != nil {
		t.Fatal(err)
	}
	if !leaseID.MatchString(l.ID) || l.Generation != 1 || l.ID == l.Token || l.Holder != "writer" {
		t.Fatal("lease identity", l.ID, l.Generation, l.Holder)
	}
	other, err := model.NewLease("writer")
	if err != nil || other.ID == l.ID {
		t.Fatal("each acquisition needs its own lease ID", err)
	}
}

func TestRotateKeepsIDAndAdvancesGeneration(t *testing.T) {
	l, err := model.NewLease("writer")
	if err != nil {
		t.Fatal(err)
	}
	l.Continuation, l.Execution, l.RetainedRuns, l.StopRequested = "c", &model.Execution{ID: "e"}, []string{"r"}, true
	r, err := l.Rotate()
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != l.ID || r.Holder != l.Holder || r.Generation != l.Generation+1 || r.Token == l.Token || r.Token == "" {
		t.Fatal("rotation identity", r.ID, r.Generation, r.Holder, "from", l.ID, l.Generation)
	}
	if r.Continuation != "" || r.Execution != nil || r.RetainedRuns != nil || r.StopRequested {
		t.Fatal("rotation copied caller-owned fields")
	}
	again, err := r.Rotate()
	if err != nil || again.Generation != 3 || again.ID != l.ID {
		t.Fatal("second rotation", again, err)
	}
}
