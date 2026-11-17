package failurelog

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
)

func capture(t *testing.T) *bytes.Buffer {
	var out bytes.Buffer
	previous, flags := log.Writer(), log.Flags()
	log.SetOutput(&out)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(previous); log.SetFlags(flags) })
	return &out
}

func TestFailureLinesCarryIdentifiersPhaseAndCause(t *testing.T) {
	out := capture(t)
	result := &protocolv2.Mutation{Workspace: protocolv2.Workspace{Key: "fresh-1", Phase: "materializing"}, Grant: &protocolv2.Grant{LeaseToken: "ab5b0c25cbd61a8a75251a2a63f96699"}}
	V2(protocolv2.Request{Operation: "create", RequestID: "fresh1-create", Key: "fresh-1"}, result, "operation_failed", errors.New("fixed-helper operation failed: exit status 1"))
	want := `operation failed: api=v2 operation="create" request_id="fresh1-create" key="fresh-1" phase="materializing" code=operation_failed cause="fixed-helper operation failed: exit status 1"` + "\n"
	if out.String() != want {
		t.Fatalf("got  %q\nwant %q", out.String(), want)
	}
	out.Reset()
	V1(protocol.Request{Operation: "create", RequestID: "v1-create", Key: "legacy-3", Arguments: []string{"secret-argument"}}, "operation_failed", errors.New("quota allocation: capacity_exhausted"))
	if line := out.String(); !strings.Contains(line, `api=v1 operation="create" request_id="v1-create" key="legacy-3" phase="-"`) || !strings.Contains(line, "capacity_exhausted") || strings.Contains(line, "secret-argument") {
		t.Fatal(line)
	}
	out.Reset()
	Receipt("v2", "run", "run-1", "ws", errors.New("cancel requested"))
	if !strings.Contains(out.String(), `code=receipt_save_failed cause="cancel requested"`) {
		t.Fatal(out.String())
	}
	out.Reset()
	V2(protocolv2.Request{}, nil, "", nil)
	if out.Len() != 0 {
		t.Fatal("logged a success", out.String())
	}
}

func TestCauseRemovesCredentialsAndIsBounded(t *testing.T) {
	cause := Cause(errors.New("git fetch https://user:hunter2@example.invalid/repo.git?access_token=t0k#frag failed\nnext"))
	if strings.Contains(cause, "hunter2") || strings.Contains(cause, "t0k") || strings.Contains(cause, "user:") || !strings.Contains(cause, `https://[redacted]@example.invalid/repo.git failed\nnext`) {
		t.Fatal(cause)
	}
	long := Cause(errors.New(strings.Repeat("é", MaxCause)))
	if len(long) > 2*MaxCause+40 || !strings.HasSuffix(long, `...[truncated]"`) {
		t.Fatal(len(long), long[len(long)-30:])
	}
}
