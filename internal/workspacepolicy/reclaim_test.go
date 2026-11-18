package workspacepolicy

import (
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

// ReleasedRecord is a released, allocated, checkpointed ready workspace: what
// a reclaim starts from. Exported for the transition test outside the package.
func ReleasedRecord(t *testing.T) *model.Bench {
	t.Helper()
	b := prepared()
	b.Workspace.Phase = "ready"
	b.Lease = nil
	b.Workspace.Checkpoint = checkpoint(t, b)
	b.Workspace.Checkpoint.Seal = strings.Repeat("f", 40)
	b.ProjectID, b.QuotaBytes, b.QuotaInodes = 10007, 4096, 128
	if err := Validate(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestReclaimPhasesKeepOnlyAReleasedCheckpointedRecord(t *testing.T) {
	for _, phase := range []string{"reclaiming", "reclaimed"} {
		b := ReleasedRecord(t)
		b.Workspace.Phase = phase
		if err := Validate(b); err != nil {
			t.Fatalf("%s: %v", phase, err)
		}
		b.Workspace.Unmerged = []model.WorkspaceMergeEvidence{}
		for name, change := range map[string]func(*model.Bench){
			"lease": func(b *model.Bench) {
				b.Lease = &model.Lease{Holder: "initialization", Token: strings.Repeat("a", 48), Continuation: strings.Repeat("b", 32)}
			},
			"pending": func(b *model.Bench) { c := *b.Workspace.Checkpoint; b.Workspace.Pending = &c },
			"stage": func(b *model.Bench) {
				b.Workspace.Stage = &model.WorkspaceStage{Name: "api", Digest: strings.Repeat("1", 64)}
			},
			"checkpoint": func(b *model.Bench) { b.Workspace.Checkpoint = nil },
			"archive":    func(b *model.Bench) { b.Workspace.Archive = &model.WorkspaceArchive{} },
			"transfer":   func(b *model.Bench) { b.Workspace.Transfer = &model.WorkspaceTransfer{} },
			"repair":     func(b *model.Bench) { b.Workspace.InitializationRepair = &model.WorkspaceInitializationRepair{} },
			"no-project": func(b *model.Bench) { b.ProjectID, b.QuotaBytes, b.QuotaInodes = 0, 0, 0 },
		} {
			c := ReleasedRecord(t)
			c.Workspace.Phase = phase
			change(c)
			if err := Validate(c); err == nil {
				t.Errorf("%s with %s accepted", phase, name)
			}
		}
	}
	if Reclaim("ready") || Reclaim("reaped") || !Reclaim("reclaiming") || !Reclaim("reclaimed") {
		t.Fatal("reclaim phases")
	}
}

// A record holds no idle timestamp, or one canonical RFC 3339 UTC time.
func TestIdleSinceIsAbsentOrCanonicalUTC(t *testing.T) {
	for value, ok := range map[string]bool{"": true, "2026-11-18T10:00:00Z": true, "2026-11-18T10:00:00.5Z": true,
		"2026-11-18T10:00:00+01:00": false, "2026-11-18 10:00:00": false, "yesterday": false, "2026-11-18T10:00:00.50Z": false} {
		b := ReleasedRecord(t)
		b.Workspace.IdleSince = value
		if err := Validate(b); (err == nil) != ok {
			t.Errorf("%q: %v", value, err)
		}
		at, held := IdleSince(b)
		if held != (ok && value != "") || held && at.Format(time.RFC3339Nano) != value {
			t.Errorf("%q: IdleSince %v %v", value, at, held)
		}
	}
}
