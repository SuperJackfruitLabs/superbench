package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestUnsupportedRequestIDRefusesBeforeEffects(t *testing.T) {
	for _, operation := range []string{"checkpoint", "release", "stop", "status", "host-status", "archive"} {
		for _, id := range []string{"", "private-request-value"} {
			for _, remote := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%t/%s", operation, remote, id), func(t *testing.T) {
					root := t.TempDir()
					state := filepath.Join(root, "must-not-create")
					args := []string{operation, "--state", state, "--request-id=" + id}
					if remote {
						args = append(args, "--socket", filepath.Join(root, "absent.sock"))
					}
					args = append(args, "card")
					err := run(args)
					want := fmt.Sprintf("--request-id is unsupported for %s: only create, recover, lease, run, and setup accept replay IDs", operation)
					if err == nil || err.Error() != want {
						t.Fatalf("wanted %q, got %v", want, err)
					}
					if _, err := os.Lstat(state); !os.IsNotExist(err) {
						t.Fatalf("state affected: %v", err)
					}
				})
			}
		}
	}
}
