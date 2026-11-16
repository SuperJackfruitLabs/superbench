package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperationcontrol"
)

func TestFixedPolicyLegacyBehaviorAndIndexedNoFallback(t *testing.T) {
	var output bytes.Buffer
	effects := 0
	effect := func(w io.Writer) error { effects++; _, err := io.WriteString(w, "legacy"); return err }
	if err := runFixedPolicy(context.Background(), "/root/custom.json", hoststorage.Policy{}, effect, &output); err != nil || output.String() != "legacy" || effects != 1 {
		t.Fatalf("legacy behavior changed: %q %d %v", output.String(), effects, err)
	}
	for _, mode := range []string{"indexed-v2", "unknown"} {
		output.Reset()
		p := hoststorage.Policy{OperationJournal: mode}
		if err := runFixedPolicy(context.Background(), "/root/custom.json", p, effect, &output); !errors.Is(err, quotaoperationcontrol.ErrPolicy) || effects != 1 || output.Len() != 0 {
			t.Fatalf("indexed override fell back: %s %d %q %v", mode, effects, output.String(), err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runFixedPolicy(ctx, quotaoperationcontrol.PolicyPath, hoststorage.Policy{OperationJournal: "indexed-v2"}, effect, &output); !errors.Is(err, context.Canceled) || effects != 1 || output.Len() != 0 {
		t.Fatalf("canceled indexed call fell back: %d %q %v", effects, output.String(), err)
	}
	if os.Getuid() == 0 {
		t.Fatal("ordinary check must run as non-root")
	}
	if err := runFixedPolicy(context.Background(), quotaoperationcontrol.PolicyPath, hoststorage.Policy{OperationJournal: "indexed-v2"}, effect, &output); !errors.Is(err, quotaoperationcontrol.ErrPolicy) || effects != 1 || output.Len() != 0 {
		t.Fatalf("unprivileged indexed call fell back: %d %q %v", effects, output.String(), err)
	}
}

func TestQuotaOutputBoundsAndShortWrite(t *testing.T) {
	var b quotaOutput
	if n, err := b.Write(bytes.Repeat([]byte{'x'}, quotahelper.MaxReply)); err != nil || n != quotahelper.MaxReply {
		t.Fatal(n, err)
	}
	if n, err := b.Write([]byte{'y'}); err == nil || n != 0 || b.buffer.Len() != quotahelper.MaxReply {
		t.Fatal("oversized output accepted", n, err)
	}
	if err := writeQuotaOutput(shortQuotaWriter{}, []byte("exact")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("short output acknowledged", err)
	}
}

type shortQuotaWriter struct{}

func (shortQuotaWriter) Write(raw []byte) (int, error) { return len(raw) - 1, nil }

// This is the deployed pre-indexed strict policy shape. An explicit indexed
// policy must be unknown to that helper, including its generic admin commands.
type legacyStoragePolicy struct {
	Root, Ledger                                          string
	OwnerUID, OwnerGID                                    int
	BenchBytes, StoreBytes, MirrorBytes, ReserveBytes     uint64
	BenchInodes, StoreInodes, MirrorInodes, ReserveInodes uint64
}

func TestIndexedPolicyFencesLegacyStrictDecoderAndDefaultEncoding(t *testing.T) {
	for _, mode := range []string{"", "indexed-v2"} {
		raw, err := json.Marshal(hoststorage.Policy{OperationJournal: mode})
		if err != nil {
			t.Fatal(err)
		}
		var old legacyStoragePolicy
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&old)
		if mode == "" && (err != nil || bytes.Contains(raw, []byte("OperationJournal"))) {
			t.Fatalf("default policy encoding changed: %s %v", raw, err)
		}
		if mode != "" && (err == nil || !strings.Contains(err.Error(), `unknown field "OperationJournal"`)) {
			t.Fatalf("old helper accepted indexed policy: %s %v", raw, err)
		}
	}
}
