package quotahelper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

func reclaimTransportFixture(t *testing.T) (Config, string, string) {
	t.Helper()
	_, c, _, commands := scopedFixture(t)
	args := filepath.Join(filepath.Dir(c.Command), "reclaim-args")
	input := filepath.Join(filepath.Dir(c.Command), "reclaim-input")
	t.Setenv("RECLAIM_TEST_ARGS", args)
	t.Setenv("RECLAIM_TEST_INPUT", input)
	t.Setenv("RECLAIM_TEST_REPLY", `{"Version":"`+ReclaimVersion+`","Key":"card","Allocation":{"Path":"`+filepath.Join(c.Data, "benches", "card")+`","ProjectID":10007,"Bytes":0,"Inodes":0}}`)
	script, err := os.ReadFile(c.Command)
	if err != nil {
		t.Fatal(err)
	}
	script = []byte(strings.Replace(string(script), "apply-operation)", `reclaim-payload) printf '%s\n' "$@" >> "$RECLAIM_TEST_ARGS"; /bin/cat > "$RECLAIM_TEST_INPUT"; printf '%s' "$RECLAIM_TEST_REPLY" ;;
apply-operation)`, 1))
	if err = os.WriteFile(c.Command, script, 0700); err != nil {
		t.Fatal(err)
	}
	var info Info
	if err = json.Unmarshal([]byte(os.Getenv("QUOTA_SCOPED_INFO")), &info); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(script)
	info.BinarySHA256 = hex.EncodeToString(sum[:])
	raw, _ := json.Marshal(info)
	t.Setenv("QUOTA_SCOPED_INFO", string(raw))
	return c, commands, args
}

// A reclaim names the same identity on every retry, derived from the
// workspace, and sends the exact Spec; nothing is recorded as a receipt
// operation.
func TestReclaimTransportRetriesUnderOneWorkspaceIdentity(t *testing.T) {
	c, commands, args := reclaimTransportFixture(t)
	id := strings.Repeat("c", 32)
	spec := []byte(`{"bounded":"spec"}`)
	for range 2 {
		got, err := reclaimScoped(context.Background(), c, "card", id, 10007, spec, 0, uint32(os.Geteuid()))
		if err != nil || got.Key != "card" || got.Allocation.ProjectID != 10007 {
			t.Fatalf("reclaim: %+v %v", got, err)
		}
	}
	raw, err := os.ReadFile(args)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if err != nil || len(lines) != 14 || strings.Join(lines[:7], "\n") != strings.Join(lines[7:], "\n") || lines[0] != "reclaim-payload" || lines[1] != "card" {
		t.Fatalf("reclaim arguments: %q %v", lines, err)
	}
	identity := ReclaimIdentity("card", id, lines[5], lines[6], lines[4])
	if !identity.Valid() || lines[2] != identity.OperationID || lines[3] != identity.Epoch {
		t.Fatalf("identity %+v from %q", identity, lines)
	}
	if other := ReclaimIdentity("card", strings.Repeat("d", 32), lines[5], lines[6], lines[4]); other.OperationID == identity.OperationID || other.Epoch == identity.Epoch {
		t.Fatal("two workspaces share a reclaim identity")
	}
	if input, err := os.ReadFile(os.Getenv("RECLAIM_TEST_INPUT")); err != nil || string(input) != string(spec) {
		t.Fatalf("spec: %q %v", input, err)
	}
	if log, err := os.ReadFile(commands); err != nil || string(log) != "operation-info\nreclaim-payload\noperation-info\nreclaim-payload\n" {
		t.Fatalf("commands: %q %v", log, err)
	}
	// A reply naming another project is refused.
	if _, err := reclaimScoped(context.Background(), c, "card", id, 10008, spec, 0, uint32(os.Geteuid())); err == nil {
		t.Fatal("a tombstone for another project accepted")
	}
}

func TestReclaimTransportRefusesBeforeTheHelper(t *testing.T) {
	c, commands, _ := reclaimTransportFixture(t)
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 7, RequestID: "typed", Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "create", Epoch: strings.Repeat("b", 32)}
	scoped, err := operationcontrol.WithScope(context.Background(), i, func(fn func() error) error { return fn() })
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("c", 32)
	override := c
	override.PolicyOverride = "/untrusted"
	for name, call := range map[string]func() error{
		"receipt-scope": func() error {
			_, err := reclaimScoped(scoped, c, "card", id, 10007, []byte("{}"), 0, uint32(os.Geteuid()))
			return err
		},
		"key": func() error {
			_, err := reclaimScoped(context.Background(), c, "../card", id, 10007, []byte("{}"), 0, uint32(os.Geteuid()))
			return err
		},
		"id": func() error {
			_, err := reclaimScoped(context.Background(), c, "card", "C"+id[1:], 10007, []byte("{}"), 0, uint32(os.Geteuid()))
			return err
		},
		"project": func() error {
			_, err := reclaimScoped(context.Background(), c, "card", id, 9999, []byte("{}"), 0, uint32(os.Geteuid()))
			return err
		},
		"empty-spec": func() error {
			_, err := reclaimScoped(context.Background(), c, "card", id, 10007, nil, 0, uint32(os.Geteuid()))
			return err
		},
		"override": func() error {
			_, err := reclaimScoped(context.Background(), override, "card", id, 10007, []byte("{}"), 0, uint32(os.Geteuid()))
			return err
		},
	} {
		if err := call(); !errors.Is(err, operationcontrol.ErrAuthority) {
			t.Errorf("%s admitted: %v", name, err)
		}
	}
	if raw, err := os.ReadFile(commands); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused reclaim reached the helper: %q %v", raw, err)
	}
	// The same fixture then reaches the helper: the refusals were the guards'.
	if _, err := reclaimScoped(context.Background(), c, "card", id, 10007, []byte("{}"), 0, uint32(os.Geteuid())); err != nil {
		t.Fatal(err)
	}
}
