package quotahelper

import (
	"context"
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
	long := "#!/bin/sh\nhead -c 4000 /dev/zero | tr '\\0' x >&2\nexit 1\n"
	if err = os.WriteFile(helper, []byte(long), 0700); err != nil {
		t.Fatal(err)
	}
	_, err = helperCommand(context.Background(), Config{Command: helper}, 0, false, nil, "bootstrap-payload")
	if err == nil || len(err.Error()) > 600 {
		t.Fatalf("helper diagnostic not bounded: %d", len(err.Error()))
	}
}
