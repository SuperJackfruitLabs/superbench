//go:build linux

package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

var recoveryCommands = []string{"plan-remove-empty-bootstrap-leaf", "remove-empty-bootstrap-leaf", "plan-repair-bootstrap-quota", "repair-bootstrap-quota", "plan-clean-bootstrap", "clean-bootstrap"}

func recoveryArgs(command string) []string {
	h := strings.Repeat("a", 64)
	return []string{command, "card", h, strings.Repeat("b", 32), h, h, h}
}

func TestRecoveryCommandsTakeTheOriginalIdentity(t *testing.T) {
	for _, command := range recoveryCommands {
		if !isRecoveryCommand(command) {
			t.Fatalf("%s is not routed", command)
		}
		key, identity, err := parseBootstrapCommand(recoveryArgs(command))
		if err != nil || key != "card" || !identity.Valid() {
			t.Fatalf("%s identity refused: %v", command, err)
		}
		for _, bad := range [][]string{recoveryArgs(command)[:6], append(recoveryArgs(command), "extra"), {command, "../card", "", "", "", "", ""}} {
			if _, _, err := parseBootstrapCommand(bad); !errors.Is(err, errBootstrapCommand) {
				t.Fatalf("%s accepted %q: %v", command, bad, err)
			}
		}
	}
	for _, other := range []string{"clean-worker", "plan-clean-bootstrap-leaf", "remove-bootstrap-leaf", "repair-bootstrap", ""} {
		if isRecoveryCommand(other) {
			t.Fatalf("%q routed as recovery", other)
		}
	}
}

// Sudo, an unprivileged caller and a policy override are all refused before the
// input is read and before any policy, journal or lock is opened.
func TestRecoveryCommandsRefuseBeforeInputAndAuthority(t *testing.T) {
	if os.Getuid() == 0 {
		t.Fatal("ordinary test requires an unprivileged user")
	}
	for _, command := range recoveryCommands {
		path := t.TempDir() + "/input"
		if err := os.WriteFile(path, []byte(`{"Spec":{}}`), 0600); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, sudo := range []string{"", "123"} {
			t.Setenv("SUDO_UID", sudo)
			for _, override := range []bool{false, true} {
				var out bytes.Buffer
				if err := runBootstrapCommand(recoveryArgs(command), override, f, &out); !errors.Is(err, errBootstrapCommand) || out.Len() != 0 {
					t.Fatalf("%s sudo=%q override=%v admitted: %v %q", command, sudo, override, err, out.Bytes())
				}
				if offset, err := f.Seek(0, 1); err != nil || offset != 0 {
					t.Fatalf("%s read input before its guard: %d %v", command, offset, err)
				}
			}
		}
		if err := run(append([]string{"--policy", "/tmp/other.json"}, recoveryArgs(command)...)); err == nil {
			t.Fatalf("%s accepted a policy override", command)
		}
		if err := run(recoveryArgs(command)); !errors.Is(err, errBootstrapCommand) {
			t.Fatalf("%s missed the guarded dispatch: %v", command, err)
		}
		f.Close()
	}
}

// The worker is selected only by its exact first argument, before any flag or
// policy parsing; an operator flag in front of it is an ordinary command.
func TestCleanWorkerIsNotARouteAfterFlags(t *testing.T) {
	if err := run([]string{"--policy", "/etc/superbench/storage.json", "clean-worker", "1", "1"}); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("clean-worker reached through flag parsing: %v", err)
	}
}
