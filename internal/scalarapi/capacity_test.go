package scalarapi

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceapi"
)

func TestCapacityErrorProjectionKeepsOnlyLogicalQuantities(t *testing.T) {
	capacity := &bench.CapacityError{NeededBytes: 6500, AvailableBytes: 6000, NeededInodes: 400, AvailableInodes: 900}
	wrapped := fmt.Errorf("private policy /private/secret uid=99: %w", capacity)
	for _, tc := range []struct {
		name, code, message string
		project             func(error) (int, string, string)
	}{
		{"scalar_v1", "operation_failed", "operation failed; inspect retained bench state and local logs", ErrorStatus},
		{"workspace_v2", "capacity_exhausted", capacity.Message(), workspaceapi.ErrorStatus},
	} {
		name, project := tc.name, tc.project
		t.Run(name, func(t *testing.T) {
			status, code, message := project(wrapped)
			if status != 422 || code != tc.code || message != tc.message || strings.Contains(message, "private") {
				t.Fatalf("wrong version-scoped projection: %d %s %s", status, code, message)
			}
			_, generic, _ := project(errors.New(capacity.Error()))
			if generic != "operation_failed" {
				t.Fatal("arbitrary text classified as capacity", generic)
			}
			_, generic, _ = project(&bench.CapacityError{NeededBytes: 1, AvailableBytes: 2, NeededInodes: 1, AvailableInodes: 2})
			if generic != "operation_failed" {
				t.Fatal("non-exhausted quantities classified as capacity", generic)
			}
			_, guarded, _ := project(errors.Join(wrapped, bench.ErrLease))
			if guarded != "forbidden" {
				t.Fatal("capacity diagnostic overrode capability refusal", guarded)
			}
		})
	}
}

func TestFreshCreateRefusalIsAnExplicitConflict(t *testing.T) {
	status, code, message := ErrorStatus(fmt.Errorf("dispatch: %w", ErrFreshCreate))
	if status != 409 || code != "conflict" || !strings.Contains(message, "fresh_payload is active") {
		t.Fatal(status, code, message)
	}
}
