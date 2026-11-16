package quotahelper

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapacityFailureAcceptsOnlyExactKnownNumericRefusal(t *testing.T) {
	want := CapacityError{NeededBytes: 6500, AvailableBytes: 6000, NeededInodes: 400, AvailableInodes: 900}
	raw := "bench-quota: " + want.Error() + "\n"
	got := capacityFailure([]byte(raw))
	if got == nil || *got != want {
		t.Fatal("exact quota refusal lost", got)
	}
	for _, bad := range []string{
		raw + "private path", strings.TrimSuffix(raw, "\n"), "warning\n" + raw,
		strings.Replace(raw, "6500", "06500", 1), strings.Replace(raw, "6500", "-1", 1),
		strings.Replace(raw, "6500", "18446744073709551616", 1),
		strings.Replace(raw, "6500", "6000", 1),
		`{"code":"capacity_exhausted","path":"private"}`,
	} {
		if capacityFailure([]byte(bad)) != nil {
			t.Fatal("untrusted helper text became a typed refusal", bad)
		}
	}
}
func TestApplyTransportsKnownCapacityFailureWithoutAssignmentOrRetry(t *testing.T) {
	dir := t.TempDir()
	helper, calls := filepath.Join(dir, "helper"), filepath.Join(dir, "calls")
	want := CapacityError{NeededBytes: 6500, AvailableBytes: 6000, NeededInodes: 400, AvailableInodes: 900}
	t.Setenv("CAPACITY_TEST_LINE", "bench-quota: "+want.Error())
	t.Setenv("CAPACITY_TEST_CALLS", calls)
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf x >> \"$CAPACITY_TEST_CALLS\"\nprintf '%s\\n' \"$CAPACITY_TEST_LINE\" >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	a, err := apply(context.Background(), Config{Command: helper, Data: dir}, "bench", "card", 0, 0)
	var capacity *CapacityError
	if !errors.As(err, &capacity) || *capacity != want || a != (Assignment{}) {
		t.Fatal("known refusal was not preserved", a, err)
	}
	raw, readErr := os.ReadFile(calls)
	if readErr != nil || string(raw) != "x" {
		t.Fatal("refusal retried allocation", string(raw), readErr)
	}
}

func TestScopedCapacityClassificationRequiresApplyExitOneAndEmptyStdout(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "helper")
	want := CapacityError{NeededBytes: 1, AvailableBytes: 2, NeededInodes: 901, AvailableInodes: 900}
	t.Setenv("CAPACITY_TEST_LINE", "bench-quota: "+want.Error())
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%s' \"$CAPACITY_TEST_STDOUT\"\nprintf '%s\\n' \"$CAPACITY_TEST_LINE\" >&2\nexit \"$CAPACITY_TEST_EXIT\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, operation, stdout, exit string
		typed                         bool
	}{
		{"apply", "apply-operation", "", "1", true},
		{"info", "operation-info", "", "1", false},
		{"cancel", "cancel-operation", "", "1", false},
		{"unknown exit", "apply-operation", "", "2", false},
		{"ambiguous stdout", "apply-operation", "{}", "1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CAPACITY_TEST_STDOUT", tc.stdout)
			t.Setenv("CAPACITY_TEST_EXIT", tc.exit)
			raw, err := helperCommand(context.Background(), Config{Command: helper}, 0, false, nil, tc.operation)
			var capacity *CapacityError
			if errors.As(err, &capacity) != tc.typed || err == nil || raw != nil {
				t.Fatal("wrong capacity classification", capacity, err)
			}
			if tc.typed && *capacity != want {
				t.Fatal("changed quantities", capacity)
			}
		})
	}
}
