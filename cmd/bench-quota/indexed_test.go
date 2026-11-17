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

func TestBufferedQuotaWithholdsSuccessfulEffectAfterFinalAuthorityFailure(t *testing.T) {
	finalErr := errors.New("actual final authority check failed")
	effects := 0
	var out bytes.Buffer
	effect := func(w io.Writer) error {
		effects++
		_, err := io.WriteString(w, "private completed effect")
		return err
	}
	// The enclosing control checks authority AFTER the actual effect succeeds.
	err := bufferedQuotaOutput(func(fn func() error) error {
		if err := fn(); err != nil {
			return err
		}
		return finalErr
	}, effect, &out)
	if !errors.Is(err, finalErr) || effects != 1 || out.Len() != 0 {
		t.Fatalf("unverified bytes escaped: %q %d %v", out.String(), effects, err)
	}
	if err = bufferedQuotaOutput(func(fn func() error) error { return fn() }, effect, &out); err != nil || effects != 2 || out.String() != "private completed effect" {
		t.Fatalf("verified publication changed: %q %d %v", out.String(), effects, err)
	}
}

func TestIndexedUnscopedMutationsRequireOriginalProvenance(t *testing.T) {
	for _, command := range []string{"apply", "release"} {
		if err := checkIndexedCommand(hoststorage.Policy{OperationJournal: "indexed-v2"}, command, "bench"); !errors.Is(err, errUnscopedIndexed) {
			t.Fatalf("%s: %v", command, err)
		}
		if err := checkIndexedCommand(hoststorage.Policy{}, command, "bench"); err != nil {
			t.Fatalf("legacy %s changed: %v", command, err)
		}
	}
	for _, command := range []string{"apply-operation", "cancel-operation", "retire-approved", "verify-retired", "verify", "inspect-allocation"} {
		if err := checkIndexedCommand(hoststorage.Policy{OperationJournal: "indexed-v2"}, command, "bench"); err != nil {
			t.Fatalf("typed/reviewed %s refused: %v", command, err)
		}
	}
}

// bench configure-host and admission apply the fixed shared projects without an
// operation scope. Refusing them made an indexed host impossible to configure.
func TestIndexedHostCanStillApplyFixedSharedProjects(t *testing.T) {
	indexed := hoststorage.Policy{OperationJournal: "indexed-v2"}
	if err := checkIndexedCommand(indexed, "apply", "shared"); err != nil {
		t.Fatal("fixed shared apply refused", err)
	}
	if err := checkIndexedCommand(indexed, "release", "shared"); !errors.Is(err, errUnscopedIndexed) {
		t.Fatal("unscoped release accepted", err)
	}
}
