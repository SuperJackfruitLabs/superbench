package transferguard_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/transferguard"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacetransfer"
)

func TestIncomingTransferBindsExplicitAndSealedRepositoryQuotas(t *testing.T) {
	f := newFixture(t, 2, func(int) string {
		return "[quota]\nsize='32MiB'\n[setup]\nonce='echo once >> once-count'\neach='echo each >> each-count'\n"
	})
	f.ready()
	_, err := f.lifecycle().Release(context.Background(), "card", f.initial.Lease.Token)
	if err != nil {
		t.Fatal(err)
	}
	manager := workspacetransfer.Manager{Store: f.store}
	plan, err := manager.Inspect(context.Background(), "card", "target", "quota-transfer", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Begin(context.Background(), *plan); err != nil {
		t.Fatal(err)
	}
	// This native fixture cannot supervise commands, but stages the real target.
	if _, err = manager.Continue(context.Background(), "card", plan.ID); err == nil {
		t.Fatal("ordinary directory supervised initialization")
	}
	var target *model.Bench
	if err = f.manager.Read(func(rows benchstate.Database) error { target = workspace.Clone(rows["target"]); return nil }); err != nil {
		t.Fatal(err)
	}
	if target == nil || target.Workspace.ExplicitQuotaBytes != 0 || target.RequestedQuotaBytes != 32<<20 {
		t.Fatal("fixture did not resolve sealed repository quota")
	}
	check := func(candidate *model.Bench) error {
		return f.update(func(_ benchstate.Database, tx *sql.Tx) error {
			return transferguard.Check(context.Background(), transferguard.Config{State: f.store.State, Data: f.store.Data, Git: f.manager.Git}, tx, candidate)
		})
	}
	if err = check(target); err != nil {
		t.Fatal("implicit sealed quota refused", err)
	}
	for name, mutate := range map[string]func(*model.Bench){
		"explicit": func(b *model.Bench) { b.Workspace.ExplicitQuotaBytes = 32 << 20 },
		"resolved": func(b *model.Bench) { b.RequestedQuotaBytes = 16 << 20 },
		"member":   func(b *model.Bench) { b.Workspace.Members[0].RequestedQuotaBytes = 16 << 20 },
		"source":   func(b *model.Bench) { b.Workspace.Members[0].Source = "ffffffffffffffffffffffffffffffffffffffff" },
		"mirror":   func(b *model.Bench) { b.Workspace.Members[0].Mirror = f.store.Data },
		"coherent-forgery": func(b *model.Bench) {
			b.RequestedQuotaBytes = 16 << 20
			for i := range b.Workspace.Members {
				b.Workspace.Members[i].RequestedQuotaBytes = 16 << 20
			}
		},
	} {
		candidate := workspace.Clone(target)
		mutate(candidate)
		if err = check(candidate); !errors.Is(err, transferguard.ErrGuard) {
			t.Fatalf("%s quota change accepted: %v", name, err)
		}
	}
}
