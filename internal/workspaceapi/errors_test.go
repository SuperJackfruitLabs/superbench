package workspaceapi_test

import (
	"errors"
	"fmt"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceapi"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceops"
)

const dirtyMessage = "workspace has uncommitted or untracked files; commit or remove them before checkpoint or release"

// A dirty tree is the one conflict an operator can act on without reading local
// logs, so it names the cause. Only a dirty check's own paths are named; any
// other wrapping keeps the static message.
func TestDirtyWorkspaceHasItsOwnStaticConflictMessage(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("private /data/benches/card/untracked.txt: %w", bench.ErrDirty),
		errors.Join(workspaceops.ErrConflict, bench.ErrDirty),
	} {
		status, code, message := workspaceapi.ErrorStatus(err)
		if status != 409 || code != "conflict" || message != dirtyMessage {
			t.Fatalf("dirty projection = %d %s %q", status, code, message)
		}
	}
	if _, _, message := workspaceapi.ErrorStatus(workspaceops.ErrConflict); message != "workspace state conflicts with this operation" {
		t.Fatalf("generic conflict changed: %q", message)
	}
	if _, code, _ := workspaceapi.ErrorStatus(errors.Join(bench.ErrDirty, bench.ErrLease)); code != "forbidden" {
		t.Fatalf("dirty diagnostic overrode capability refusal: %s", code)
	}
	for _, op := range []string{"release", "checkpoint"} {
		r := protocolv2.Request{Version: protocolv2.Version, Operation: op, Key: "card", LeaseToken: "0123456789abcdef0123456789abcdef"}
		response, status, err := workspaceapi.Envelope(r, nil, protocolv2.Authority{}, bench.ErrDirty)
		if err != nil || status != 409 || response.Error == nil || response.Error.Message != dirtyMessage {
			t.Fatalf("%s envelope = %d %+v %v", op, status, response.Error, err)
		}
	}
}

// The refusal names the first offending paths relative to the workspace root,
// counts the rest, and stays within the protocol's message bound.
func TestDirtyWorkspaceNamesOffendingPaths(t *testing.T) {
	dirty := model.DirtyStatus("api", "M src/app.py\n?? src/__pycache__/a.pyc\n?? src/__pycache__/b.pyc\n?? src/__pycache__/c.pyc\n?? src/__pycache__/d.pyc\n?? src/__pycache__/e.pyc\n?? src/__pycache__/f.pyc")
	err := errors.Join(workspaceops.ErrConflict, fmt.Errorf("private /data/benches/card: %w", dirty))
	status, code, message := workspaceapi.ErrorStatus(err)
	want := dirtyMessage + ": api/src/app.py, api/src/__pycache__/a.pyc, api/src/__pycache__/b.pyc, api/src/__pycache__/c.pyc, api/src/__pycache__/d.pyc and 2 more"
	if status != 409 || code != "conflict" || message != want || len(message) > 256 {
		t.Fatalf("dirty projection = %d %s %q", status, code, message)
	}
	r := protocolv2.Request{Version: protocolv2.Version, Operation: "checkpoint", Key: "card", LeaseToken: "0123456789abcdef0123456789abcdef"}
	response, status, err := workspaceapi.Envelope(r, nil, protocolv2.Authority{}, err)
	if err != nil || status != 409 || response.Error == nil || response.Error.Code != "conflict" || response.Error.Message != want {
		t.Fatalf("checkpoint envelope = %d %+v %v", status, response.Error, err)
	}
}
