package gitcontrol

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedCommandNeutralizesConfiguredProgramsAndPinsDot(t *testing.T) {
	client, _ := fixture(t)
	marker := filepath.Join(filepath.Dir(client), "program-ran")
	program := filepath.Join(filepath.Dir(client), "external-program")
	hook(t, program, marker, false)
	must(t, client, "config", "fixture.marker", marker)
	for _, key := range []string{"diff.external", "diff.fixture.command", "diff.fixture.textconv", "core.sshCommand", "credential.helper", "credential.https://example.invalid.helper", "core.alternateRefsCommand"} {
		must(t, client, "config", key, program)
	}
	if err := os.WriteFile(filepath.Join(client, "tracked"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(client, ".gitattributes"), []byte("tracked diff=fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := ManagedCommand(context.Background(), client, "diff", "HEAD")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "diff --git") {
		t.Fatalf("managed diff failed: %v %s", err, out)
	}
	if cmd.Dir != client {
		t.Fatal("lost daemon registration directory")
	}
	for i, arg := range cmd.Args {
		if arg == "-C" && (i+1 == len(cmd.Args) || cmd.Args[i+1] != ".") {
			t.Fatalf("Git re-resolves checkout: %q", cmd.Args)
		}
	}
	for key, want := range map[string]string{"core.hooksPath": "/dev/null", "core.fsmonitor": "false", "push.followTags": "false", "core.sshCommand": "/usr/bin/ssh -F /dev/null", "credential.helper": "", "core.alternateRefsCommand": ""} {
		out, err := ManagedCommand(context.Background(), client, "config", "--get", key).CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != want {
			t.Fatalf("effective %s policy: %q %v", key, out, err)
		}
	}
	credential := ManagedCommand(context.Background(), client, "credential", "fill")
	credential.Stdin = strings.NewReader("protocol=https\nhost=example.invalid\n\n")
	if out, err := credential.CombinedOutput(); err == nil || !strings.Contains(string(out), "terminal prompts disabled") {
		t.Fatalf("credential refusal changed: %q %v", out, err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("configured helper executed: %v", err)
	}
}
