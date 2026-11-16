//go:build linux

package payloadlaunch

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os/exec"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
)

func TestExplicitPayloadInputIsCopiedAndArrivesOnlyInsideFiniteEnvelope(t *testing.T) {
	data := bytes.Repeat([]byte{0, 255, 'x'}, 30000)
	original := bytes.Clone(data)
	cmd := exec.Command("trusted-command", "literal")
	var output bytes.Buffer
	cmd.Stdout = &output
	b, err := PreparePayloadInput(cmd, targetFixture("payload"), scopeFixture(), PayloadInput{Bytes: data, Limit: uint64(len(data))})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for i := range data {
		data[i] = 'z'
	}
	startBridge(t, b, "echo-frame")
	if err := b.WaitLeader(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := bounded(t)
	defer cancel()
	fences := 0
	if err := b.Drain(ctx, fenceFunc(func(_ *execution.Execution) error { fences++; return nil })); err != nil {
		t.Fatal(err)
	}
	raw := output.Bytes()
	if fences != 1 || len(raw) < 4 || int(binary.BigEndian.Uint32(raw[:4])) != len(raw)-4 {
		t.Fatal("missing fence or extra streamed payload bytes")
	}
	got, err := payloadexec.ParseRequest(raw[4:])
	if err != nil || !bytes.Equal(got.Input, original) {
		t.Fatalf("finite input mutated or absent: %v", err)
	}
}
func TestExplicitPayloadInputRefusesUntrustedLimitsKindAndStdinBeforeMutation(t *testing.T) {
	cases := []struct {
		name  string
		kind  string
		input PayloadInput
		stdin bool
	}{
		{"missing policy limit", "payload", PayloadInput{Bytes: []byte{1}}, false},
		{"caller limit widening", "payload", PayloadInput{Limit: payloadexec.MaxPayloadInput + 1}, false},
		{"input beyond policy", "payload", PayloadInput{Bytes: []byte{1, 2}, Limit: 1}, false},
		{"Git ambiguity", "git", PayloadInput{Bytes: []byte{1}, Limit: 1}, false},
		{"arbitrary stdin", "payload", PayloadInput{Bytes: []byte{1}, Limit: 1}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd := exec.Command("original")
			if c.stdin {
				cmd.Stdin = bytes.NewReader([]byte("not adopted"))
			}
			before := cmd.Stdin
			b, err := PreparePayloadInput(cmd, targetFixture(c.kind), scopeFixture(), c.input)
			if b != nil || !errors.Is(err, ErrTransport) || cmd.Stdin != before || cmd.Path != "original" || cmd.Stdout != nil {
				t.Fatalf("refusal mutated command: %v", err)
			}
		})
	}
}
func TestEmptyExplicitPayloadInputPreservesLegacyEnvelope(t *testing.T) {
	first, err := Prepare(exec.Command("original"), targetFixture("payload"), scopeFixture(), GitInput{})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := PreparePayloadInput(exec.Command("original"), targetFixture("payload"), scopeFixture(), PayloadInput{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if !bytes.Equal(first.input, second.input) {
		t.Fatal("empty payload changed legacy envelope/EOF")
	}
}
