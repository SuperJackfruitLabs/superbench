package workspaceapi_test

import (
	"errors"
	"fmt"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceapi"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceops"
)

const dirtyMessage = "workspace has uncommitted or untracked files; commit or remove them before checkpoint or release"

// A dirty tree is the one conflict an operator can act on without reading local
// logs, so it names the cause. The message stays static: no path or file name.
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
