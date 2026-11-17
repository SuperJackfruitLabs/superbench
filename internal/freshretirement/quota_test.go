package freshretirement

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestUnusedRequiresUniqueZeroUsageAndOriginalOrUninitializedCap(t *testing.T) {
	for _, raw := range []string{"#10000 0 0 16384\n", "#10000 0 0 0\n"} {
		if err := unused([]byte(raw), 10000, 16384); err != nil {
			t.Fatalf("unique unused project refused: %q %v", raw, err)
		}
	}
	for name, raw := range map[string]string{
		"missing": "", "foreign": "#10001 0 0 16384\n", "truncated": "#10000 0 0\n",
		"duplicate": "#10000 0 0 16384\n#10000 0 0 16384\n", "used": "#10000 1 0 16384\n",
		"truncated-byte-duplicate":  "#10000 0 0 16384\n#10000 99\n",
		"truncated-inode-duplicate": "#10000 0 0 0\n#10000\n",
		"unlinked-inode":            "#10000 1 0 4096\n", "invalid-usage": "#10000 x 0 16384\n",
		"foreign-cap": "#10000 0 0 8192\n", "invalid-cap": "#10000 0 0 x\n",
		"foreign-soft": "#10000 0 1 16384\n", "invalid-soft": "#10000 0 x 16384\n",
	} {
		t.Run(name, func(t *testing.T) {
			if err := unused([]byte(raw), 10000, 16384); !errors.Is(err, ErrRetirement) {
				t.Fatalf("ambiguous/live quota admitted: %q %v", raw, err)
			}
		})
	}
}

func TestFixedOutputCannotGrowPastBound(t *testing.T) {
	var out reportOutput
	raw := bytes.Repeat([]byte{'x'}, 64<<10)
	if n, err := out.Write(raw); n != len(raw) || err != nil {
		t.Fatalf("bounded output refused: %d %v", n, err)
	}
	if n, err := out.Write([]byte{'y'}); n != 0 || !errors.Is(err, ErrRetirement) || !bytes.Equal(out.raw, raw) {
		t.Fatalf("oversize output retained: %d %v len%d", n, err, len(out.raw))
	}
}

func TestRetirementSyncsDataBeforeTrustingAbsenceForTheTombstone(t *testing.T) {
	var calls []string
	step := func(name string, fail bool) func() error {
		return func() error {
			calls = append(calls, name)
			if fail {
				return ErrRetirement
			}
			return nil
		}
	}
	if err := ordered(step("absent", false), step("unused", false), step("sync", false), step("save", false)); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(calls, " "); got != "absent unused sync absent save absent" {
		t.Fatalf("retirement order changed: %s", got)
	}
	calls = nil
	if err := ordered(step("absent", false), step("unused", false), step("sync", false), nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(calls, " "); got != "absent unused sync absent" {
		t.Fatalf("observation order changed: %s", got)
	}
	calls = nil
	if err := ordered(step("absent", false), step("unused", false), step("sync", true), step("save", false)); !errors.Is(err, ErrRetirement) {
		t.Fatalf("failed sync ignored: %v", err)
	}
	if got := strings.Join(calls, " "); got != "absent unused sync" {
		t.Fatalf("tombstone reachable after a failed sync: %s", got)
	}
}

// Only the reclaim entry points admit the fixed helper's sudo caller; absent
// retirement and recovery selection stay direct root.
func TestOnlyReclaimAdmitsTheSudoCaller(t *testing.T) {
	for _, c := range []struct {
		uid, euid int
		sudo      string
		helper    bool
		want      bool
	}{
		{0, 0, "", false, true}, {0, 0, "", true, true},
		{0, 0, "1000", false, false}, {0, 0, "1000", true, true},
		{1000, 0, "", true, false}, {0, 1000, "", true, false}, {1000, 1000, "1000", true, false},
	} {
		if got := admitted(c.uid, c.euid, c.sudo, c.helper); got != c.want {
			t.Errorf("admitted(%d, %d, %q, %v) = %v", c.uid, c.euid, c.sudo, c.helper, got)
		}
	}
}
