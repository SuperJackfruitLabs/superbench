package quotahelper

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleasePreservesFixedTransportAndExitEvidence(t *testing.T) {
	dir := t.TempDir()
	helper, args := filepath.Join(dir, "helper"), filepath.Join(dir, "args")
	t.Setenv("RELEASE_TEST_ARGS", args)
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$RELEASE_TEST_ARGS\"\nprintf '%s' \"$RELEASE_TEST_REPLY\"\nexit \"$RELEASE_TEST_EXIT\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RELEASE_TEST_EXIT", "0")
	t.Setenv("RELEASE_TEST_REPLY", `{"Path":"/data/benches/card","ProjectID":123,"Bytes":0,"Inodes":0}`)
	c := Config{Command: helper, PolicyOverride: "/private/fixed-policy"}
	a, err := release(c, "card", 123, 0)
	if err != nil || a.Path != "/data/benches/card" || a.ProjectID != 123 || a.Bytes != 0 || a.Inodes != 0 {
		t.Fatal("retirement reply changed", a, err)
	}
	raw, err := os.ReadFile(args)
	if err != nil || string(raw) != "--policy\n/private/fixed-policy\nrelease\nbench\ncard\n123\n" {
		t.Fatal("fixed retirement argv changed", string(raw), err)
	}
	t.Setenv("RELEASE_TEST_EXIT", "7")
	t.Setenv("RELEASE_TEST_REPLY", "deliberate retirement refusal")
	_, err = release(c, "card", 123, 0)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 || !strings.Contains(err.Error(), "quota release:") || !strings.Contains(err.Error(), "deliberate retirement refusal") {
		t.Fatal("retirement failure evidence changed", err)
	}
}
