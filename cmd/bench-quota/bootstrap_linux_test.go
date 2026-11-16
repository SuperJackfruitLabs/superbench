//go:build linux

package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
)

func TestBootstrapCommandBindsExactOriginalIdentityBeforePolicyRead(t *testing.T) {
	h := strings.Repeat("a", 64)
	epoch := strings.Repeat("b", 32)
	valid := []string{"bootstrap-payload", "card", h, epoch, h, h, h}
	key, identity, err := parseBootstrapCommand(valid)
	if err != nil || key != "card" || !identity.Valid() {
		t.Fatalf("valid command refused: %s %+v %v", key, identity, err)
	}
	inspection := append([]string{}, valid...)
	inspection[0] = "inspect-bootstrap-payload"
	if gotKey, gotIdentity, err := parseBootstrapCommand(inspection); err != nil || gotKey != key || gotIdentity != identity {
		t.Fatalf("inspection lost exact original binding: %s %+v %v", gotKey, gotIdentity, err)
	}
	for _, bad := range [][]string{
		valid[:6], append(append([]string{}, valid...), "extra"),
		{"bootstrap-payload", "../escape", h, epoch, h, h, h},
		{"bootstrap-payload", "card", h, epoch, h, h, ""},
		{"bootstrap-payload", "card", strings.ToUpper(h), epoch, h, h, h},
		{"inspect-bootstrap-payload", "../escape", h, epoch, h, h, h},
		{"inspect-bootstrap-payload", "card", h, epoch, h, h, ""},
		{"unknown", "card", h, epoch, h, h, h},
	} {
		if _, _, err := parseBootstrapCommand(bad); !errors.Is(err, errBootstrapCommand) {
			t.Fatalf("invalid identity accepted: %q %v", bad, err)
		}
	}
	var out bytes.Buffer
	if err = runBootstrapCommand(valid, true, nil, &out); !errors.Is(err, errBootstrapCommand) || out.Len() != 0 {
		t.Fatalf("policy override admitted: %q %v", out.String(), err)
	}
	if err = runBootstrapCommand([]string{"initialize-indexed", "extra"}, false, nil, &out); !errors.Is(err, errBootstrapCommand) || out.Len() != 0 {
		t.Fatalf("initializer args admitted: %q %v", out.String(), err)
	}
}

func TestBootstrapInspectionDirectRootGuardPrecedesInputAndPolicy(t *testing.T) {
	t.Setenv("SUDO_UID", "123")
	h := strings.Repeat("a", 64)
	args := []string{"inspect-bootstrap-payload", "card", h, strings.Repeat("b", 32), h, h, h}
	spec := payloadbootstrap.Spec{Version: payloadbootstrap.Version, Key: "card", ID: strings.Repeat("a", 32),
		CheckpointRepository: "api",
		Members:              []payloadbootstrap.Member{{Name: "api", Repository: "https://example.invalid/api.git", Branch: "task/api", Source: strings.Repeat("a", 40), ObjectFormat: "sha1"}}}
	raw, err := spec.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/spec"
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out bytes.Buffer
	if err := runBootstrapCommand(args, false, f, &out); !errors.Is(err, errBootstrapCommand) || out.Len() != 0 {
		t.Fatalf("sudo inspection reached installed authority: %v %q", err, out.Bytes())
	}
	if offset, err := f.Seek(0, 1); err != nil || offset != 0 {
		t.Fatalf("sudo inspection read input: %d %v", offset, err)
	}
	if err := run(args); !errors.Is(err, errBootstrapCommand) {
		t.Fatalf("inspection command missed guarded dispatch: %v", err)
	}
}

func TestBootstrapInputPreservesBytesAndRejectsOversizeBeforeAuthority(t *testing.T) {
	path := t.TempDir() + "/spec"
	data := []byte(`{"Version":"synthetic"}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := readBootstrapSpec(f)
	f.Close()
	if err != nil || !bytes.Equal(raw, data) {
		t.Fatalf("finite input changed: %q %v", raw, err)
	}
	if err = os.WriteFile(path, bytes.Repeat([]byte("x"), payloadbootstrap.MaxSpec+1), 0600); err != nil {
		t.Fatal(err)
	}
	f, err = os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if raw, err = readBootstrapSpec(f); !errors.Is(err, errBootstrapCommand) || raw != nil {
		t.Fatalf("oversize input accepted: %d %v", len(raw), err)
	}
}

func TestBootstrapPartialPipeCannotHoldPrivilegedInputReader(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if _, err = w.Write([]byte("{")); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	raw, err := readBootstrapSpecUntil(r, start.Add(30*time.Millisecond))
	if !errors.Is(err, errBootstrapCommand) || raw != nil || time.Since(start) > time.Second {
		t.Fatalf("partial input was not bounded: %d %v", len(raw), err)
	}
}
