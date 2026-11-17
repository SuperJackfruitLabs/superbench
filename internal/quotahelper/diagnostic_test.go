package quotahelper

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A Fresh create on a legacy storage policy failed with only "exit status 1":
// the helper's own refusal line was discarded, so nothing could log the cause.
func TestScopedHelperFailureKeepsTheHelpersRefusal(t *testing.T) {
	helper := filepath.Join(t.TempDir(), "bench-quota")
	script := "#!/bin/sh\nprintf 'bench-quota: fresh storage initialization refused\\nsecond line\\n' >&2\nexit 1\n"
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	_, err := helperCommand(context.Background(), Config{Command: helper}, 0, false, nil, "bootstrap-payload")
	if err == nil || !strings.Contains(err.Error(), `exit status 1: "bench-quota: fresh storage initialization refused"`) || strings.Contains(err.Error(), "second line") {
		t.Fatalf("helper refusal lost or unbounded: %v", err)
	}
	// Control bytes from the helper must not reach the journal raw.
	escape := "#!/bin/sh\nprintf 'bench-quota: refused \\033[2J\\r\\tdone\\n' >&2\nexit 1\n"
	if err = os.WriteFile(helper, []byte(escape), 0700); err != nil {
		t.Fatal(err)
	}
	_, err = helperCommand(context.Background(), Config{Command: helper}, 0, false, nil, "bootstrap-payload")
	if err == nil || !strings.Contains(err.Error(), `"bench-quota: refused \x1b[2J\r\tdone"`) || strings.ContainsAny(err.Error(), "\x1b\r\t") {
		t.Fatalf("helper control bytes not quoted: %q", err)
	}
	long := "#!/bin/sh\nhead -c 4000 /dev/zero | tr '\\0' x >&2\nexit 1\n"
	if err = os.WriteFile(helper, []byte(long), 0700); err != nil {
		t.Fatal(err)
	}
	_, err = helperCommand(context.Background(), Config{Command: helper}, 0, false, nil, "bootstrap-payload")
	if err == nil || len(err.Error()) > 600 {
		t.Fatalf("helper diagnostic not bounded: %d", len(err.Error()))
	}
}

// A Fresh create past the volume's reservations reached the client as
// operation_failed: the bootstrap transport never typed the helper's canonical
// capacity refusal, so the API could not answer capacity_exhausted.
func TestBootstrapCapacityRefusalIsTyped(t *testing.T) {
	helper := filepath.Join(t.TempDir(), "bench-quota")
	line := "bench-quota: capacity_exhausted: logical reservation capacity exhausted; needed_bytes=18253611008 available_bytes=17112760320 needed_inodes=4250000 available_inodes=8388608"
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '"+line+"\\n' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"bootstrap-payload", "apply-operation"} {
		_, err := helperCommand(context.Background(), Config{Command: helper}, 0, false, nil, command)
		var capacity *CapacityError
		if !errors.As(err, &capacity) || capacity.NeededBytes != 18253611008 || capacity.AvailableInodes != 8388608 {
			t.Fatalf("%s capacity refusal not typed: %v", command, err)
		}
	}
	if _, err := helperCommand(context.Background(), Config{Command: helper}, 0, false, nil, "operation-info"); errors.As(err, new(*CapacityError)) {
		t.Fatal("a non-allocating command was typed as a capacity refusal")
	}
}
