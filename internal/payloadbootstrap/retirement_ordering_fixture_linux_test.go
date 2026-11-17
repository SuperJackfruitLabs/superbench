//go:build linux && freshstoragefixture

package payloadbootstrap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperationcontrol"
)

// Only the maintenance gate is replaced. The actual installed manager, denied
// journal, intent, approval file and XFS effect remain in this disposable fixture.
// Synthetic interrupted intents here do not prove a booted maintenance lifecycle.
func TestRetirementOrderingFixture(t *testing.T) {
	if os.Getenv("SUPERBENCH_FRESH_LOWER_FIXTURE") != "1" || os.Getuid() != 0 || os.Geteuid() != 0 || os.Getenv("SUDO_UID") != "" {
		t.Fatal("dedicated direct-root lower fixture required")
	}
	control, p, err := quotaoperationcontrol.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(p.Ledger, "payload-bootstrap", "registered.json"))
	if err != nil {
		t.Fatal(err)
	}
	var original intent
	if err = json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	hash := func(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
	for _, kind := range []string{"persist-before-effect", "original-binding", "post-effect-binding", "success"} {
		t.Run(kind, func(t *testing.T) {
			s := original.Spec
			s.Key = "ordering-" + kind
			s.ID = hash(s.Key)[:32]
			s.QuotaBytes = 4 << 20
			identity := original.Operation
			identity.OperationID = hash(s.Key)
			identity.StateIdentity = hash("state:" + s.Key)
			m, err := Open(t.Context(), control, identity)
			if err != nil {
				t.Fatal(err)
			}
			stopped := errors.New("fixture stops before ownership initialization")
			var allocation hoststorage.Assignment
			err = m.store.Apply(t.Context(), func() error {
				_, err := p.ApplyFreshRequested(s.Key, s.QuotaBytes, func() error {
					return m.write(intent{Spec: s, Operation: identity, PolicyDigest: m.digest, Phase: "reserved"}, true)
				}, func(a hoststorage.Assignment) error { allocation = a; return stopped })
				return err
			})
			if !errors.Is(err, stopped) {
				t.Fatal(err)
			}
			info, err := os.Lstat(allocation.Path)
			if err != nil {
				t.Fatal(err)
			}
			i, err := m.read(s)
			if err != nil {
				t.Fatal(err)
			}
			// Valid retained metadata for an interrupted allocating phase. No checkout,
			// payload ownership transfer or completed bootstrap is claimed by this setup.
			i.Phase = "allocating"
			i.Allocation = allocation
			i.Root = node(info)
			i.Controls = node(info)
			if err = m.store.Apply(t.Context(), func() error { return m.write(i, false) }); err != nil {
				t.Fatal(err)
			}
			if err = os.Remove(allocation.Path); err != nil {
				t.Fatal(err)
			}
			if _, err = m.store.Cancel(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err = m.PlanRetirement(t.Context(), s); !errors.Is(err, quotaoperationcontrol.ErrInstall) {
				t.Fatalf("production gate not used: %v", err)
			}
			calls := 0
			m.maintenanceGate = func(context.Context, hoststorage.Policy) error { calls++; return nil }
			if err = m.retirementReady(t.Context(), s); err != nil {
				t.Fatal(err)
			}
			t.Setenv("SUDO_UID", "123")
			if err = m.retirementReady(t.Context(), s); !errors.Is(err, ErrBootstrap) {
				t.Fatalf("sudo uid bypassed direct-root guard: %v", err)
			}
			if _, err = m.PlanRetirement(t.Context(), s); !errors.Is(err, ErrBootstrap) || calls != 0 {
				t.Fatalf("sudo reached gate: %v calls%d", err, calls)
			}
			t.Setenv("SUDO_UID", "")
			plan, err := m.PlanRetirement(t.Context(), s)
			if err != nil {
				t.Fatal(err)
			}
			calls = 0
			approvalPath := filepath.Join(p.Ledger, "payload-retirements", s.Key+".json")
			ledgerPath := filepath.Join(p.Ledger, "assignments.json")
			before, err := os.ReadFile(ledgerPath)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "persist-before-effect":
				if err = os.MkdirAll(filepath.Dir(approvalPath), 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(approvalPath, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "original-binding":
				plan.Bootstrap.IntentSHA256 = hash("foreign retained intent")
			case "post-effect-binding":
				m.maintenanceGate = func(context.Context, hoststorage.Policy) error {
					calls++
					if calls == 3 {
						changed := i
						changed.CommandSHA256 = hash("changed after effect")
						return m.write(changed, false)
					}
					return nil
				}
			}
			got, err := m.RetireAbsent(t.Context(), s, plan)
			after, readErr := os.ReadFile(ledgerPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			switch kind {
			case "persist-before-effect", "original-binding":
				if !errors.Is(err, ErrBootstrap) || got != (hoststorage.Assignment{}) {
					t.Fatalf("invalid approval acknowledged: %+v %v", got, err)
				}
				if !bytes.Equal(before, after) {
					t.Fatal("retirement preceded durable bound approval")
				}
				saved, e := os.ReadFile(approvalPath)
				if kind == "persist-before-effect" {
					if e != nil || string(saved) != "{" {
						t.Fatal("partial approval changed", e)
					}
				} else if !errors.Is(e, os.ErrNotExist) {
					t.Fatal("foreign intent approval persisted", e)
				}
			case "post-effect-binding":
				if !errors.Is(err, ErrBootstrap) || !strings.Contains(err.Error(), "effects may have occurred; retry the same plan") || got != (hoststorage.Assignment{}) {
					t.Fatalf("changed intent not reported after effect: %+v %v", got, err)
				}
				if calls != 3 {
					t.Fatalf("post-effect change not injected: %d", calls)
				}
				assertRetirementTombstone(t, after, s.Key, allocation)
			case "success":
				if err != nil || got.ProjectID != allocation.ProjectID || got.Bytes != 0 || got.Inodes != 0 {
					t.Fatalf("valid retirement: %+v %v", got, err)
				}
				assertRetirementTombstone(t, after, s.Key, allocation)
			}
			if kind == "success" || kind == "post-effect-binding" {
				saved, e := os.ReadFile(approvalPath)
				want, _ := json.Marshal(plan)
				if e != nil || !bytes.Equal(saved, want) {
					t.Fatal("effect lacks exact retained approval", e)
				}
			}
		})
	}
	t.Log("RETIREMENT_ORDERING_AND_BINDING_PASS")
}

func assertRetirementTombstone(t *testing.T, raw []byte, key string, a hoststorage.Assignment) {
	t.Helper()
	var l struct {
		Assignments map[string]hoststorage.Assignment
	}
	if err := json.Unmarshal(raw, &l); err != nil {
		t.Fatal(err)
	}
	a.Bytes, a.Inodes = 0, 0
	if l.Assignments["bench:"+key] != a {
		t.Fatal("retirement did not commit exact tombstone")
	}
}
