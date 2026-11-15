package quotahelper

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrdinaryTransportPreservesExactFixedArgumentsAndAssignment(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "helper")
	record := filepath.Join(dir, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$QUOTA_TEST_RECORD\"\nprintf '%s' \"$QUOTA_TEST_RESULT\"\n"
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	c := Config{Command: command, Data: filepath.Join(dir, "data"), PolicyOverride: filepath.Join(dir, "root-policy.json")}
	expected := Assignment{Path: filepath.Join(c.Data, "benches", "card"), ProjectID: 10000, Bytes: 4096, Inodes: 9}
	raw, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("QUOTA_TEST_RECORD", record)
	t.Setenv("QUOTA_TEST_RESULT", string(raw))
	// This selects the direct-command branch in an owned fake helper fixture;
	// it changes no process identity and invokes no sudo or real quota helper.
	got, err := apply(context.Background(), c, "bench", "card", 4096, 0)
	if err != nil || got != expected {
		t.Fatal(got, err)
	}
	args, err := os.ReadFile(record)
	want := "--policy\n" + c.PolicyOverride + "\napply\nbench\ncard\n4096\n"
	if err != nil || string(args) != want {
		t.Fatal(string(args), err)
	}
	c.PolicyOverride = ""
	expected.Path = filepath.Join(c.Data, "mirrors")
	raw, err = json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("QUOTA_TEST_RESULT", string(raw))
	got, err = apply(nil, c, "shared", "mirrors", 0, 0)
	if err != nil || got != expected {
		t.Fatal(got, err)
	}
	args, err = os.ReadFile(record)
	if err != nil || string(args) != "apply\nshared\nmirrors\n" {
		t.Fatal(string(args), err)
	}
}
func TestOrdinaryTransportPreservesExitFailureAndRejectsConflictingAssignment(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "helper")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nprintf 'deliberate failure' >&2\nexit 3\n"), 0700); err != nil {
		t.Fatal(err)
	}
	c := Config{Command: command, Data: filepath.Join(dir, "data")}
	_, err := apply(nil, c, "bench", "card", 0, 0)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 || !strings.Contains(err.Error(), "quota allocation:") || !strings.Contains(err.Error(), "deliberate failure") {
		t.Fatal(err)
	}
	if err := os.WriteFile(command, []byte("#!/bin/sh\nprintf '{\"Path\":\"/unregistered\",\"ProjectID\":10000,\"Bytes\":4096,\"Inodes\":9}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = apply(nil, c, "bench", "card", 4096, 0); err == nil {
		t.Fatal("conflicting helper assignment adopted")
	}
}
