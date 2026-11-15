package workspacepolicy

import (
	"fmt"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func TestInitializationRepairMarkerValidatesBoundedMonotonicInventoryShape(t *testing.T) {
	b := validRecord()
	b.Workspace.Phase = "initializing"
	b.Workspace.InitializationRepair = &model.WorkspaceInitializationRepair{ID: "repair1", Digest: strings.Repeat("1", 64), UncertainOnce: []string{"api"}}
	if err := Validate(b); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSchema19(b); err == nil {
		t.Fatal("historical schema19 adopted marker")
	}
	for i := 0; i < 10000; i++ {
		b.Workspace.InitializationRepair.RetiredRuns = append(b.Workspace.InitializationRepair.RetiredRuns, fmt.Sprintf("%032x", i))
	}
	if err := Validate(b); err != nil {
		t.Fatal("bounded full history inventory refused", err)
	}
	b.Workspace.InitializationRepair.RetiredRuns = append(b.Workspace.InitializationRepair.RetiredRuns, strings.Repeat("f", 32))
	if err := Validate(b); err == nil {
		t.Fatal("unbounded retired history accepted")
	}
}

func TestRepairMarkerRefusesDuplicateUnknownOrLiveEvidence(t *testing.T) {
	changes := map[string]func(*model.Bench){
		"unknown-once":   func(b *model.Bench) { b.Workspace.InitializationRepair.UncertainOnce = []string{"unknown"} },
		"duplicate-once": func(b *model.Bench) { b.Workspace.InitializationRepair.UncertainOnce = []string{"api", "api"} },
		"duplicate-run": func(b *model.Bench) {
			b.Workspace.InitializationRepair.RetiredRuns = []string{strings.Repeat("2", 32), strings.Repeat("2", 32)}
		},
		"live-run": func(b *model.Bench) {
			b.Lease.RetainedRuns = []string{strings.Repeat("2", 32)}
			b.Workspace.InitializationRepair.RetiredRuns = []string{strings.Repeat("2", 32)}
		},
		"same-previous": func(b *model.Bench) { b.Workspace.InitializationRepair.PreviousID = "repair1" },
		"materializing": func(b *model.Bench) { b.Workspace.Phase = "materializing" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			b := validRecord()
			b.Workspace.Phase = "initializing"
			b.Workspace.InitializationRepair = &model.WorkspaceInitializationRepair{ID: "repair1", Digest: strings.Repeat("1", 64)}
			change(b)
			if err := Validate(b); err == nil {
				t.Fatal("invalid repair marker accepted")
			}
		})
	}
}
