//go:build darwin

package hostidentity

import (
	"context"
	"strings"
	"testing"
)

func TestNativeDarwinIdentityStableAndCancelledObservationRefused(t *testing.T) {
	first, err := Current(context.Background())
	if err != nil || len(first) != 64 {
		t.Fatal("native platform identity unavailable", err)
	}
	second, err := Current(context.Background())
	if err != nil || first != second {
		t.Fatal("native platform identity changed between observations", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Current(ctx); err == nil {
		t.Fatal("cancelled inspection returned an identity")
	}
}

func TestHardwareCommandOutputIsBoundedWhileDrained(t *testing.T) {
	var out boundedOutput
	input := []byte(strings.Repeat("x", 1<<20))
	n, err := out.Write(input)
	if err != nil || n != len(input) || !out.overflow || out.Len() != 64<<10 {
		t.Fatal("unbounded or undrained hardware observation")
	}
	if n, err := out.Write(input); err != nil || n != len(input) || out.Len() != 64<<10 {
		t.Fatal("subsequent output grew the retained observation")
	}
}
