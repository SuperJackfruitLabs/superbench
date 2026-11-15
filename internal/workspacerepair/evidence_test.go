package workspacerepair_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/output"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/setup"
)

func TestClaimRefusesStaleRowsHeadReceiptsAndFenceWithoutTakeover(t *testing.T) {
	for _, kind := range []string{"row", "head", "receipt", "identity"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t, nil)
			f.stop()
			p := f.inspect()
			switch kind {
			case "row":
				must(t, f.sql(func(tx *sql.Tx) error {
					_, err := tx.Exec("UPDATE benches SET record=json_set(record,'$.Lease.Token',?)", strings.Repeat("f", 48))
					return err
				}))
			case "head":
				git(t, f.initial.Workspace.Members[1].Path, "commit", "--allow-empty", "-m", "new work after approval")
			case "receipt":
				must(t, f.sql(func(tx *sql.Tx) error {
					return setup.Record(tx, "workspace:"+f.initial.Workspace.ID+":b", strings.Repeat("f", 64), strings.Repeat("e", 32))
				}))
			case "identity":
				must(t, os.Rename(f.initial.Workspace.Members[1].Path, f.initial.Workspace.Members[1].Path+"-moved"))
			}
			before := f.stateBytes()
			if out, err := f.m.Claim(context.Background(), *p, "repair1"); err == nil || out != nil {
				t.Fatal("changed approved evidence acquired a fresh owner")
			}
			if !bytes.Equal(before, f.stateBytes()) || f.backend.count() != 0 {
				t.Fatal("refused claim changed authority or launched effects")
			}
		})
	}
}

func TestClaimRefusesChangedCaptureAndIndependentFenceFailure(t *testing.T) {
	for _, kind := range []string{"capture", "fence"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t, func(int) string { return "[setup]\neach = 'true'\n" })
			seal := f.m.Prep.Seal
			f.m.Prep.Seal = func(id string, c *output.Capture, exit *int, truncated bool) error {
				if err := seal(id, c, exit, truncated); err != nil {
					return err
				}
				_, err := f.m.Prep.Stop("card", f.initial.Lease.Token, false)
				return err
			}
			if _, err := f.m.Prep.Prepare(context.Background(), "card", f.initial.Lease.Token, "a", false); err == nil {
				t.Fatal("fixture did not retain stopped successful stage")
			}
			p := f.inspect()
			if kind == "capture" {
				must(t, os.WriteFile(f.row().Lease.Execution.Log, []byte("changed capture"), 0600))
			} else {
				f.backend.mu.Lock()
				f.backend.fenceErr = errors.New("fixture fence refused")
				f.backend.mu.Unlock()
			}
			before := f.stateBytes()
			if out, err := f.m.Claim(context.Background(), *p, "repair1"); err == nil || out != nil {
				t.Fatal("changed capture/fence failure acquired owner")
			}
			if !bytes.Equal(before, f.stateBytes()) {
				t.Fatal("refused takeover changed ownership or history")
			}
		})
	}
}

func TestPendingGenerationRepairResumesExactVectorWithoutHooks(t *testing.T) {
	f := newFixture(t, hooks)
	_, err := f.m.Prep.Prepare(context.Background(), "card", f.initial.Lease.Token, "", false)
	must(t, err)
	hook := filepath.Join(f.spec.Repositories[0].Repository, ".git", "hooks", "pre-receive")
	must(t, os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0700))
	if _, err := f.m.Lifecycle.Checkpoint(context.Background(), "card", f.initial.Lease.Token); err == nil {
		t.Fatal("publication fixture did not fail")
	}
	pending := f.row().Workspace.Pending
	if pending == nil {
		t.Fatal("publication failure lost durable generation")
	}
	f.stop()
	p := f.inspect()
	if p.Pending == nil || p.Pending.GenerationID != pending.GenerationID || p.Members[0].Action != "preserve" || p.Members[1].Action != "preserve" {
		t.Fatal("pending repair changed frozen preparation")
	}
	owner := f.claim(p, "repair1")
	starts := f.backend.count()
	must(t, os.Remove(hook))
	out, err := f.m.Continue(context.Background(), "card", "repair1", owner.GrantToken)
	must(t, err)
	b := f.row()
	if out.Phase != "ready" || b.Workspace.Pending != nil || b.Workspace.Checkpoint.GenerationID != pending.GenerationID || f.backend.count() != starts || f.count(0, "once") != 1 || f.count(1, "once") != 1 {
		t.Fatal("pending repair selected a new generation or reran setup")
	}
}
