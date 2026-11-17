//go:build linux && freshstoragefixture

package payloadbootstrap_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshrecovery/fixture"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperationjournal"
)

func assertLowerRetainedInspection(t *testing.T, control *quotaoperationjournal.Control, p hoststorage.Policy, spec payloadbootstrap.Spec, identity quotaoperation.Identity, result payloadbootstrap.Result, helper, sha string) {
	t.Helper()
	manager, err := payloadbootstrap.Open(t.Context(), control, identity)
	if err != nil {
		t.Fatal(err)
	}
	intentPath := filepath.Join(p.Ledger, "payload-bootstrap", spec.Key+".json")
	intent, err := os.ReadFile(intentPath)
	if err != nil {
		t.Fatal(err)
	}
	assignments, err := os.ReadFile(filepath.Join(p.Ledger, "assignments.json"))
	if err != nil {
		t.Fatal(err)
	}
	specRaw, err := spec.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	observed, err := manager.Inspect(t.Context(), spec)
	if err != nil || observed.Version != payloadbootstrap.InspectionVersion || observed.Key != spec.Key || observed.ID != spec.ID || observed.Operation != identity || observed.OperationDenied || observed.Phase != "completed" || observed.RecordedAllocation != result.Allocation || observed.RecordedRoot != result.Layout.Root || observed.PolicyDigest != result.Layout.PolicyDigest || observed.IntentSHA256 != lowerHash(string(intent)) || observed.SpecSHA256 != lowerHash(string(specRaw)) {
		t.Fatalf("inspection differs from actual retained intent: %+v %v", observed, err)
	}
	// An unknown identity must not become a reservation merely to inspect a file.
	indexed := filepath.Join(p.Ledger, "operations", "indexed-v2")
	db, journal := filepath.Join(indexed, "journal.db"), filepath.Join(indexed, "journal.db-journal")
	dbRaw, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	journalRaw, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	foreign := identity
	foreign.OperationID = lowerHash("never-admitted-bootstrap-inspection")
	unknown, err := payloadbootstrap.Open(t.Context(), control, foreign)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := unknown.Inspect(t.Context(), spec); !errors.Is(err, payloadbootstrap.ErrBootstrap) || !reflect.DeepEqual(got, payloadbootstrap.Inspection{}) {
		t.Fatalf("unknown identity exposed intent: %+v %v", got, err)
	}
	assertLowerBytes(t, db, dbRaw)
	assertLowerBytes(t, journal, journalRaw)
	changed := spec
	changed.Members = append([]payloadbootstrap.Member{}, spec.Members...)
	changed.Members[0].Source = strings.Repeat("f", 40)
	if got, err := manager.Inspect(t.Context(), changed); !errors.Is(err, payloadbootstrap.ErrBootstrap) || !reflect.DeepEqual(got, payloadbootstrap.Inspection{}) {
		t.Fatalf("foreign source vector exposed intent: %+v %v", got, err)
	}
	store, err := control.Operation(t.Context(), identity, func(i quotaoperation.Identity) error {
		verifyLowerHelper(t, helper, sha)
		authority, err := p.OperationAuthority(t.Context())
		if err != nil {
			return err
		}
		if i != identity || i.Authority != authority {
			return quotaoperation.ErrAuthority
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Cancel(t.Context()); err != nil {
		t.Fatal(err)
	}
	denied, err := manager.Inspect(t.Context(), spec)
	want := observed
	want.OperationDenied = true
	if err != nil || denied != want {
		t.Fatalf("denied retained evidence changed: %+v want %+v %v", denied, want, err)
	}
	if err := manager.Begin(t.Context(), spec); !errors.Is(err, quotaoperation.ErrCancelled) {
		t.Fatalf("inspection revived cancelled bootstrap: %v", err)
	}
	assertLowerBytes(t, intentPath, intent)
	assertLowerBytes(t, filepath.Join(p.Ledger, "assignments.json"), assignments)
	if got, err := manager.PlanRetirement(t.Context(), spec); !errors.Is(err, quotaoperationcontrol.ErrInstall) || got != (payloadbootstrap.RetirementPlan{}) {
		t.Fatalf("unbooted lower fixture granted maintenance: %+v %v", got, err)
	}
	assertLowerBytes(t, intentPath, intent)
	assertLowerBytes(t, filepath.Join(p.Ledger, "assignments.json"), assignments)
	child := exec.CommandContext(t.Context(), helper, "-test.v", "-test.run=^Test(Retirement(ApprovalCustody|Ordering)|FreshRecovery)Fixture$", "-test.timeout=0")
	log, err := child.CombinedOutput()
	if err != nil || (!bytes.Contains(log, []byte("RETIREMENT_APPROVAL_CUSTODY_PASS")) || !bytes.Contains(log, []byte("RETIREMENT_ORDERING_AND_BINDING_PASS"))) || fixture.Logged(log) != nil || bytes.Contains(log, []byte("SKIP")) {
		t.Fatalf("actual root approval custody failed: %v\n%s", err, log)
	}
	fixture.Relay(t, log)
	t.Log("LOWER_RETAINED_BOOTSTRAP_INSPECTION_WITHOUT_REVIVAL_PASS")
}

// The recovery child: real leaks recovered through the real commands' Manager calls.
func TestFreshRecoveryFixture(t *testing.T) { fixture.Run(t, payloadbootstrap.SetFixtureGate) }
