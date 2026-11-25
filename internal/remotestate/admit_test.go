package remotestate

import (
	"errors"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteca"
)

func TestAdmitReturnsTheBenchRoot(t *testing.T) {
	f := newFixture(t)
	g := f.grant(t)
	a, err := f.m.Admit(g.Claims)
	b := f.record(t)
	if err != nil || a.Key != "card" || a.BenchID != b.Workspace.ID || a.LayoutID != b.Workspace.ID || a.Root != b.Path || a.Holder != "worker" || a.Claims != g.Claims || a.Execution != *b.Lease.Execution {
		t.Fatal("admission", a, err)
	}
}

func TestAdmitRefusesEveryMismatch(t *testing.T) {
	f := newFixture(t)
	g := f.grant(t)
	claims := func(change func(*remoteca.Claims)) remoteca.Claims {
		c := g.Claims
		change(&c)
		return c
	}
	for name, c := range map[string]remoteca.Claims{
		"unknown-bench":       claims(func(c *remoteca.Claims) { c.BenchID = strings.Repeat("0", 32) }),
		"empty-bench":         claims(func(c *remoteca.Claims) { c.BenchID = "" }),
		"lease-id":            claims(func(c *remoteca.Claims) { c.LeaseID = strings.Repeat("0", 32) }),
		"empty-lease-id":      claims(func(c *remoteca.Claims) { c.LeaseID = "" }),
		"older-generation":    claims(func(c *remoteca.Claims) { c.Generation-- }),
		"newer-generation":    claims(func(c *remoteca.Claims) { c.Generation++ }),
		"execution-id":        claims(func(c *remoteca.Claims) { c.ExecutionID = strings.Repeat("0", 32) }),
		"empty-execution-id":  claims(func(c *remoteca.Claims) { c.ExecutionID = "" }),
		"execution-id-prefix": claims(func(c *remoteca.Claims) { c.ExecutionID = c.ExecutionID[:31] }),
	} {
		if _, err := f.m.Admit(c); !errors.Is(err, ErrRevoked) {
			t.Errorf("%s admitted: %v", name, err)
		}
	}
	// The live record changes underneath unchanged claims.
	ready := f.record(t)
	for name, change := range map[string]func(*model.Bench){
		"stop-requested": func(b *model.Bench) { b.Lease.StopRequested = true },
		"execution-nil":  func(b *model.Bench) { b.Lease.Execution = nil },
		"kind-empty":     func(b *model.Bench) { b.Lease.Execution.Kind = "" },
		"stopped":        func(b *model.Bench) { b.Lease.Execution.Phase = "stopped" },
		"starting":       func(b *model.Bench) { b.Lease.Execution.Phase = "starting" },
		"no-lease":       func(b *model.Bench) { b.Lease = nil },
		"not-ready":      func(b *model.Bench) { b.Workspace.Phase = "archived" },
		"pending":        func(b *model.Bench) { c := *b.Workspace.Checkpoint; b.Workspace.Pending = &c },
		"transfer":       func(b *model.Bench) { b.Workspace.Transfer = &model.WorkspaceTransfer{} },
	} {
		b := *ready
		w, l := *ready.Workspace, *ready.Lease
		e := *ready.Lease.Execution
		l.Execution = &e
		b.Workspace, b.Lease = &w, &l
		change(&b)
		if _, err := admitted(map[string]*model.Bench{"card": &b}, g.Claims); !errors.Is(err, ErrRevoked) {
			t.Errorf("%s admitted: %v", name, err)
		}
	}
	// Two records claiming one bench ID is never resolved by map order.
	twin := *ready
	twin.Key = "twin"
	if _, err := admitted(map[string]*model.Bench{"card": ready, "twin": &twin}, g.Claims); !errors.Is(err, ErrRevoked) {
		t.Error("duplicate bench ID admitted", err)
	}
	if _, err := f.m.Admit(g.Claims); err != nil {
		t.Fatal("unchanged claims refused", err)
	}
}
