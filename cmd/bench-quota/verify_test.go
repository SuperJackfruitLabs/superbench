package main

import (
	"strings"
	"testing"
)

func TestVerifyCommandAcceptsOnlyFixedLogicalBenchShapeBeforePolicyRead(t *testing.T) {
	for _, args := range [][]string{{"verify"}, {"verify", "shared", "store"}, {"verify", "bench", "source", "/arbitrary/root"}, {"verify", "bench"}, {"verify", "bench", "source", "10000", "1024"}} {
		if err := run(args); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Fatalf("non-fixed verify shape reached policy or effects: %q: %v", args, err)
		}
	}
}

func TestInspectAllocationAcceptsOnlyReadOnlyLogicalKeyShape(t *testing.T) {
	for _, args := range [][]string{{"inspect-allocation"}, {"inspect-allocation", "shared", "mirrors"}, {"inspect-allocation", "bench"}, {"inspect-allocation", "bench", "card", "10000"}, {"inspect-allocation", "bench", "card", "/arbitrary/path"}} {
		if err := run(args); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Fatalf("non-fixed allocation lookup reached policy or effects: %q: %v", args, err)
		}
	}
}
