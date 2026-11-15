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
