package service

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
)

func TestCapacityErrorProjectionKeepsOnlyLogicalQuantities(t *testing.T) {
	capacity := &bench.CapacityError{NeededBytes: 6500, AvailableBytes: 6000, NeededInodes: 400, AvailableInodes: 900}
	wrapped := fmt.Errorf("private policy /private/secret uid=99: %w", capacity)
	for name, project := range map[string]func(error) (int, string, string){"scalar": operationError, "workspace": operationErrorV2} {
		t.Run(name, func(t *testing.T) {
			status, code, message := project(wrapped)
			if status != 422 || code != "capacity_exhausted" || message != capacity.Message() || strings.Contains(message, "private") {
				t.Fatalf("wrong typed projection: %d %s %s", status, code, message)
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
