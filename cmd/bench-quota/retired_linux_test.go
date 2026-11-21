//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// The read-only retired inspection takes the key alone, never an identity,
// path or policy override, and refuses any other shape before reading input.
func TestRetiredInspectionTakesOnlyTheKey(t *testing.T) {
	var out bytes.Buffer
	for _, bad := range [][]string{
		{"inspect-retired-payload"},
		{"inspect-retired-payload", ""},
		{"inspect-retired-payload", strings.Repeat("k", 81)},
		{"inspect-retired-payload", "card", "extra"},
	} {
		if err := runBootstrapCommand(bad, false, nil, &out); !errors.Is(err, errBootstrapCommand) || out.Len() != 0 {
			t.Errorf("%q admitted: %v", bad, err)
		}
	}
	if err := runRetiredInspection(context.Background(), []string{"inspect-retired-payload", "card"}, nil, &out); !errors.Is(err, errBootstrapCommand) || out.Len() != 0 {
		t.Fatalf("missing input admitted: %v", err)
	}
	if err := runBootstrapCommand([]string{"inspect-retired-payload", "card"}, true, nil, &out); !errors.Is(err, errBootstrapCommand) || out.Len() != 0 {
		t.Fatalf("policy override admitted: %v", err)
	}
	if err := run([]string{"--policy", "/tmp/other.json", "inspect-retired-payload", "card"}); err == nil {
		t.Fatal("a policy override reached the inspection")
	}
}

// The read-only absent inspection has the same shape: the key alone.
func TestAbsentInspectionTakesOnlyTheKey(t *testing.T) {
	var out bytes.Buffer
	for _, bad := range [][]string{
		{"inspect-absent-payload"},
		{"inspect-absent-payload", ""},
		{"inspect-absent-payload", strings.Repeat("k", 81)},
		{"inspect-absent-payload", "card", "extra"},
	} {
		if err := runBootstrapCommand(bad, false, nil, &out); !errors.Is(err, errBootstrapCommand) || out.Len() != 0 {
			t.Errorf("%q admitted: %v", bad, err)
		}
	}
	if err := runAbsentInspection(context.Background(), []string{"inspect-absent-payload", "card"}, nil, &out); !errors.Is(err, errBootstrapCommand) || out.Len() != 0 {
		t.Fatalf("missing input admitted: %v", err)
	}
	if err := runBootstrapCommand([]string{"inspect-absent-payload", "card"}, true, nil, &out); !errors.Is(err, errBootstrapCommand) || out.Len() != 0 {
		t.Fatalf("policy override admitted: %v", err)
	}
	if err := run([]string{"--policy", "/tmp/other.json", "inspect-absent-payload", "card"}); err == nil {
		t.Fatal("a policy override reached the inspection")
	}
}
