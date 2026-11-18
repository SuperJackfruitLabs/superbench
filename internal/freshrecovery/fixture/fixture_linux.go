//go:build linux && freshstoragefixture

// Package fixture is the privileged Fresh recovery fixture. The lower XFS
// fixture runs it as the child selector TestFreshRecoveryFixture after its
// retained-inspection assertion, in the same private namespaces, /etc and XFS.
// It is compiled only with the freshstoragefixture tag and is never part of a
// shipped binary. Leaks are made with the real quota, journal and Manager
// APIs; interrupted intents are synthesised exactly as the ordering fixture
// does. Only the booted maintenance gate is replaced, by the caller's hook.
// It then reclaims completed benches made through the real Bootstrap, as the
// payload account, under the reclaim operation's own identity (reclaim_linux.go).
// On failure every leak is retained as evidence.
package fixture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshrecovery"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshretirement"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperationjournal"
)

// Gate replaces only the manager's booted maintenance gate.
type Gate func(*payloadbootstrap.Manager, func(context.Context, hoststorage.Policy) error)

// Markers are logged once each, in this order, by a passing Run.
var Markers = []string{
	"FRESH_RECOVERY_PRODUCTION_GATE_AND_SUDO_REFUSED_PASS",
	"FRESH_RECOVERY_RESERVED_ABSENT_REPAIR_RETIRE_PASS",
	"FRESH_RECOVERY_RESERVED_EMPTY_PROJECT_ZERO_PASS",
	"FRESH_RECOVERY_RESERVED_EMPTY_ASSIGNED_PROJECT_PASS",
	"FRESH_RECOVERY_ALLOCATING_EMPTY_LEAF_PASS",
	"FRESH_RECOVERY_ALLOCATING_FIXED_PREFIX_PASS",
	"FRESH_RECOVERY_NONEMPTY_LEAF_REFUSED_PASS",
	"FRESH_RECOVERY_WRONG_PHASE_REFUSED_PASS",
	"FRESH_RECOVERY_REPAIR_FOREIGN_CAPS_REFUSED_PASS",
	"FRESH_CLEANUP_STAGED_GIT_TREES_PASS",
	"FRESH_CLEANUP_ALLOCATING_C_PASS",
	"FRESH_CLEANUP_CHANGED_ENTRY_REFUSED_PASS",
	"FRESH_CLEANUP_SYMLINK_SWAP_REFUSED_PASS",
	"FRESH_CLEANUP_UNREADABLE_REFUSED_PASS",
	"FRESH_CLEANUP_WORKER_KILLED_RETRY_PASS",
	"FRESH_CLEANUP_CRASH_AFTER_RETENTION_RETRY_PASS",
	"FRESH_CLEANUP_TRANSFERRING_PARKED_PASS",
	"FRESH_RECOVERY_ALL_LEAKS_RETIRED_PASS",
	"FRESH_RECLAIM_REFUSED_BEFORE_RETENTION_PASS",
	"FRESH_RECLAIM_REFUSED_WHILE_IN_USE_PASS",
	"FRESH_RECLAIM_ESCAPED_FILE_REFUSED_PASS",
	"FRESH_RECLAIM_DIRECTORY_WITHOUT_INHERIT_REFUSED_PASS",
	"FRESH_RECLAIM_SAME_PROJECT_OUTSIDE_LINK_BLOCKS_RETIRE_PASS",
	"FRESH_RECLAIM_HOSTILE_TREE_RETIRED_PASS",
	"FRESH_RECLAIM_LOST_REPLY_RETRY_PASS",
	"FRESH_RECLAIM_CAPACITY_RETURNED_PASS",
	"FRESH_RECLAIM_PURGE_WORKER_CONFINED_PASS",
	"FRESH_RECLAIM_KILLED_WORKER_RETRY_PASS",
}

// Logged requires the child's log to hold every marker exactly once.
func Logged(log []byte) error {
	for _, marker := range Markers {
		if n := bytes.Count(log, []byte(marker)); n != 1 {
			return fmt.Errorf("recovery marker %s logged %d times", marker, n)
		}
	}
	return nil
}

// Relay logs, in the caller's (parent) test, each marker the child emitted
// exactly once. It logs nothing the child did not log; call it only after
// Logged has accepted the child's log.
func Relay(t *testing.T, log []byte) {
	t.Helper()
	for _, marker := range Markers {
		if bytes.Count(log, []byte(marker)) == 1 {
			t.Log(marker)
		}
	}
}

type node struct{ Device, Inode uint64 }

// record has the field order and types of the manager's private intent, so its
// canonical encoding is the manager's. A mismatch fails the manager's strict read.
type record struct {
	Spec                payloadbootstrap.Spec
	Allocation          hoststorage.Assignment
	Operation           quotaoperation.Identity
	PolicyDigest, Phase string
	Root, Controls      node
	Materialized        *struct{}
	CommandSHA256       string
	CommandCompleted    bool
}

type env struct {
	t        *testing.T
	control  *quotaoperationjournal.Control
	p        hoststorage.Policy
	template record
	gate     Gate
	retired  int
}

type leak struct {
	spec     payloadbootstrap.Spec
	manager  *payloadbootstrap.Manager
	a        hoststorage.Assignment
	intent   record
	gateHits *int
}

func hash(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }

// Run proves every recovery path on real leaks, each to a retired tombstone:
// root-owned leaves (PR 2) and daemon-owned trees through the clean worker.
func Run(t *testing.T, gate Gate) {
	if os.Getenv("SUPERBENCH_FRESH_LOWER_FIXTURE") != "1" || os.Getuid() != 0 || os.Geteuid() != 0 || os.Getenv("SUDO_UID") != "" || gate == nil {
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
	e := &env{t: t, control: control, p: p, gate: gate}
	if err = json.Unmarshal(raw, &e.template); err != nil || e.template.PolicyDigest == "" {
		t.Fatal("registered intent template", err)
	}
	e.productionGateAndSudo()
	e.reservedAbsent()
	e.reservedEmpty("recovery-reserved-zero", true)
	e.reservedEmpty("recovery-reserved-assigned", false)
	e.allocatingEmpty()
	e.allocatingFixedPrefix()
	e.nonEmptyRefused()
	e.wrongPhaseRefused()
	e.repairForeignCaps()
	e.cleanups()
	// syncfs before retirement is order-tested by freshretirement's unit test;
	// this fixture only shows every leak reached a committed tombstone with it.
	if e.retired != 9+cleanupLeaks {
		t.Fatalf("%d of %d leaks retired", e.retired, 9+cleanupLeaks)
	}
	t.Log("FRESH_RECOVERY_ALL_LEAKS_RETIRED_PASS")
	e.reclaims()
}

// newLeak reserves a real Fresh allocation under a real operation, stops it
// before ownership initialization, and leaves the operation permanently denied.
// The intent stays reserved; the leaf is root 0700 with the assigned project
// and the exact limits, as applyLedger leaves it when initialize fails.
func (e *env) newLeak(key string) *leak { e.t.Helper(); return e.newLeakSized(key, 4<<20) }

// newLeakSized is newLeak with the given quota bytes.
func (e *env) newLeakSized(key string, quota uint64) *leak {
	t := e.t
	t.Helper()
	s := e.template.Spec
	s.Key, s.ID, s.QuotaBytes = key, hash("recovery-bid:" + key)[:32], quota
	identity := e.template.Operation
	identity.OperationID = hash("recovery-operation:" + key)
	identity.StateIdentity = hash("recovery-state:" + key)
	store, err := e.control.Operation(t.Context(), identity, func(i quotaoperation.Identity) error {
		authority, err := e.p.OperationAuthority(t.Context())
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
	l := &leak{spec: s, gateHits: new(int)}
	l.intent = record{Spec: s, Operation: identity, PolicyDigest: e.template.PolicyDigest, Phase: "reserved"}
	stopped := errors.New("fixture stops before fresh ownership initialization")
	err = store.Apply(t.Context(), func() error {
		_, err := e.p.ApplyFreshRequested(key, s.QuotaBytes, func() error { return e.writeIntent(l.intent, true) }, func(a hoststorage.Assignment) error { l.a = a; return stopped })
		return err
	})
	if !errors.Is(err, stopped) || l.a.ProjectID < 10000 {
		t.Fatalf("fresh leak not retained: %+v %v", l.a, err)
	}
	if l.manager, err = payloadbootstrap.Open(t.Context(), e.control, identity); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Cancel(t.Context()); err != nil {
		t.Fatal(err)
	}
	hits := l.gateHits
	e.gate(l.manager, func(context.Context, hoststorage.Policy) error { *hits++; return nil })
	return l
}

// writeIntent publishes a synthetic interrupted intent as the manager's write
// does: an exclusive fsynced file, renamed over the old one after the first.
func (e *env) writeIntent(r record, first bool) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	path := filepath.Join(e.p.Ledger, "payload-bootstrap", r.Spec.Key+".json")
	target := path
	if !first {
		target = path + ".transition"
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil && !first {
		err = os.Rename(target, path)
	}
	if err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// advance rewrites the leak's intent to phase, recording the leaf as Begin does.
func (e *env) advance(l *leak, phase string) {
	e.t.Helper()
	info := mustInfo(e.t, l.a.Path)
	st := info.Sys().(*syscall.Stat_t)
	l.intent.Phase, l.intent.Allocation = phase, l.a
	l.intent.Root = node{uint64(st.Dev), st.Ino}
	l.intent.Controls = l.intent.Root
	if err := e.writeIntent(l.intent, false); err != nil {
		e.t.Fatal(err)
	}
	if got, err := l.manager.Inspect(e.t.Context(), l.spec); err != nil || got.Phase != phase || !got.OperationDenied {
		e.t.Fatalf("synthetic %s intent unreadable: %+v %v", phase, got, err)
	}
}

func (e *env) xfsQuota(command string) {
	e.t.Helper()
	cmd := exec.Command("/usr/sbin/xfs_quota", "-x", "-c", command, e.p.Root)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	if out, err := cmd.CombinedOutput(); err != nil {
		e.t.Fatalf("fixture xfs_quota %q: %s %v", command, out, err)
	}
}

func (e *env) report(id uint32, unit string) string {
	e.t.Helper()
	cmd := exec.Command("/usr/sbin/xfs_quota", "-x", "-c", fmt.Sprintf("report -p -n %s -N -L %d -U %d", unit, id, id), e.p.Root)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	out, err := cmd.Output()
	if err != nil {
		e.t.Fatalf("fixture report: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// clearLimits models a crash before the limit command: with no usage and no
// limits the kernel reports no row for the project at all.
func (e *env) clearLimits(l *leak) {
	e.t.Helper()
	e.xfsQuota(fmt.Sprintf("limit -p bsoft=0 bhard=0 isoft=0 ihard=0 %d", l.a.ProjectID))
	if b, i := e.report(l.a.ProjectID, "-b"), e.report(l.a.ProjectID, "-i"); b != "" || i != "" {
		e.t.Fatalf("uninitialized project still reports rows: %q %q", b, i)
	}
}

// exactRows requires one zero-usage row per unit with the original hard caps.
func (e *env) exactRows(l *leak) {
	e.t.Helper()
	for unit, hard := range map[string]uint64{"-b": l.a.Bytes / 1024, "-i": l.a.Inodes} {
		f := strings.Fields(e.report(l.a.ProjectID, unit))
		if len(f) < 4 || f[0] != fmt.Sprintf("#%d", l.a.ProjectID) || f[1] != "0" || f[2] != "0" || f[3] != fmt.Sprint(hard) {
			e.t.Fatalf("project %d %s row is not the exact original cap: %q", l.a.ProjectID, unit, f)
		}
	}
}

func (e *env) approval(l *leak, step string) string {
	return filepath.Join(e.p.Ledger, "payload-cleanups", l.spec.Key+"."+step+".json")
}

// removeLeaf plans, reviews and removes the leaf, then retries the same plan.
func (e *env) removeLeaf(l *leak) freshrecovery.LeafPlan {
	t := e.t
	t.Helper()
	plan, err := freshrecovery.PlanLeaf(t.Context(), l.manager, l.spec)
	if err != nil {
		t.Fatalf("leaf plan refused: %v", err)
	}
	e.request(plan, l, freshrecovery.ParseLeaf)
	want, _ := json.Marshal(plan)
	before := *l.gateHits
	got, err := freshrecovery.RemoveLeaf(t.Context(), l.manager, l.spec, plan)
	if err != nil || got.ApprovalSHA256 != hash(string(want)) || got.Key != l.spec.Key {
		t.Fatalf("leaf removal failed: %+v %v", got, err)
	}
	if *l.gateHits < before+3 {
		t.Fatalf("leaf removal skipped the maintenance gate: %d", *l.gateHits-before)
	}
	if _, err = os.Lstat(l.a.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("leaf remains: %v", err)
	}
	assertBytes(t, e.approval(l, "leaf"), want)
	again, err := freshrecovery.RemoveLeaf(t.Context(), l.manager, l.spec, plan)
	if err != nil || again != got {
		t.Fatalf("lost-reply leaf retry: %+v %v", again, err)
	}
	return plan
}

// request round-trips plan through the command's canonical {Spec, Plan} codec.
func (e *env) request(plan any, l *leak, parse any) {
	e.t.Helper()
	raw, err := json.Marshal(struct {
		Spec payloadbootstrap.Spec
		Plan any
	}{l.spec, plan})
	if err != nil {
		e.t.Fatal(err)
	}
	switch parse := parse.(type) {
	case func([]byte) (payloadbootstrap.Spec, freshrecovery.LeafPlan, error):
		_, _, err = parse(raw)
	case func([]byte) (payloadbootstrap.Spec, freshrecovery.RepairPlan, error):
		_, _, err = parse(raw)
	}
	if err != nil {
		e.t.Fatalf("reviewed request does not parse: %v", err)
	}
}

// repair plans, reviews and repairs the quota, then retries the same plan.
func (e *env) repair(l *leak) {
	t := e.t
	t.Helper()
	plan, err := freshrecovery.PlanRepair(t.Context(), l.manager, l.spec)
	if err != nil {
		t.Fatalf("repair plan refused: %v", err)
	}
	if plan.Limit != (freshrecovery.Limit{ProjectID: l.a.ProjectID, BlockHardKiB: l.a.Bytes / 1024, InodeHard: l.a.Inodes}) {
		t.Fatalf("repair plan caps differ from the original: %+v", plan.Limit)
	}
	e.request(plan, l, freshrecovery.ParseRepair)
	got, err := freshrecovery.Repair(t.Context(), l.manager, l.spec, plan)
	if err != nil {
		t.Fatalf("repair failed: %v", err)
	}
	e.exactRows(l)
	want, _ := json.Marshal(plan)
	assertBytes(t, e.approval(l, "repair"), want)
	again, err := freshrecovery.Repair(t.Context(), l.manager, l.spec, plan)
	if err != nil || again != got {
		t.Fatalf("lost-reply repair retry: %+v %v", again, err)
	}
	e.exactRows(l)
}

// retire runs the merged absent retirement through the real Manager methods and
// checks the committed tombstone and its exact retry.
func (e *env) retire(l *leak) {
	t := e.t
	t.Helper()
	plan, err := l.manager.PlanRetirement(t.Context(), l.spec)
	if err != nil {
		t.Fatalf("absent retirement plan refused: %v", err)
	}
	got, err := l.manager.RetireAbsent(t.Context(), l.spec, plan)
	if err != nil || got.ProjectID != l.a.ProjectID || got.Path != l.a.Path || got.Bytes != 0 || got.Inodes != 0 {
		t.Fatalf("retirement failed: %+v %v", got, err)
	}
	var ledger struct {
		Assignments map[string]hoststorage.Assignment
	}
	raw, err := os.ReadFile(filepath.Join(e.p.Ledger, "assignments.json"))
	if err != nil || json.Unmarshal(raw, &ledger) != nil || ledger.Assignments["bench:"+l.spec.Key] != got {
		t.Fatalf("tombstone not committed: %v", err)
	}
	if again, err := l.manager.RetireAbsent(t.Context(), l.spec, plan); err != nil || again != got {
		t.Fatalf("retirement retry: %+v %v", again, err)
	}
	e.retired++
	// A retired row cannot be selected for another recovery step.
	if _, err := freshrecovery.PlanRepair(t.Context(), l.manager, l.spec); err == nil {
		t.Fatal("retired allocation selected for repair")
	}
}

func refused(t *testing.T, err error, what string) {
	t.Helper()
	if !errors.Is(err, freshrecovery.ErrRecovery) && !errors.Is(err, freshretirement.ErrRetirement) && !errors.Is(err, payloadbootstrap.ErrBootstrap) {
		t.Fatalf("%s was not refused: %v", what, err)
	}
}

func (e *env) productionGateAndSudo() {
	t := e.t
	l := e.newLeak("recovery-gate")
	e.gate(l.manager, quotaoperationcontrol.CheckMaintenance)
	if _, err := freshrecovery.PlanLeaf(t.Context(), l.manager, l.spec); !errors.Is(err, quotaoperationcontrol.ErrInstall) {
		t.Fatalf("production gate not consulted: %v", err)
	}
	hits := l.gateHits
	e.gate(l.manager, func(context.Context, hoststorage.Policy) error { *hits++; return nil })
	t.Setenv("SUDO_UID", "123")
	if _, err := freshrecovery.PlanLeaf(t.Context(), l.manager, l.spec); !errors.Is(err, payloadbootstrap.ErrBootstrap) || *hits != 0 {
		t.Fatalf("sudo reached the gate: %v %d", err, *hits)
	}
	t.Setenv("SUDO_UID", "")
	plan := e.removeLeaf(l)
	if len(plan.Leaf.Fixed) != 0 || plan.Leaf.ProjectID != l.a.ProjectID {
		t.Fatalf("reserved leaf plan: %+v", plan.Leaf)
	}
	e.retire(l)
	t.Log("FRESH_RECOVERY_PRODUCTION_GATE_AND_SUDO_REFUSED_PASS")
}

// reserved, leaf absent, no report rows: the crash fell before limit.
func (e *env) reservedAbsent() {
	t := e.t
	l := e.newLeak("recovery-reserved-absent")
	if err := os.Remove(l.a.Path); err != nil {
		t.Fatal(err)
	}
	e.clearLimits(l)
	if _, err := l.manager.PlanRetirement(t.Context(), l.spec); err == nil {
		t.Fatal("retirement accepted a project without report rows")
	}
	_, err := freshrecovery.PlanLeaf(t.Context(), l.manager, l.spec)
	refused(t, err, "leaf plan for an absent leaf")
	e.repair(l)
	e.retire(l)
	t.Log("FRESH_RECOVERY_RESERVED_ABSENT_REPAIR_RETIRE_PASS")
}

// reserved, an empty root 0700 leaf with project ID 0 (crash between mkdir and
// project -s, limits never set) or with the assigned ID and exact limits.
func (e *env) reservedEmpty(key string, zero bool) {
	t := e.t
	l := e.newLeak(key)
	if zero {
		if err := os.Remove(l.a.Path); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(l.a.Path, 0700); err != nil {
			t.Fatal(err)
		}
		e.clearLimits(l)
	}
	_, err := freshrecovery.PlanRepair(t.Context(), l.manager, l.spec)
	refused(t, err, "repair with the leaf present")
	plan := e.removeLeaf(l)
	want := l.a.ProjectID
	if zero {
		want = 0
	}
	if plan.Leaf.ProjectID != want || plan.Leaf.Node.UID != 0 || plan.Leaf.Node.Mode&0o7777 != 0o700 {
		t.Fatalf("reserved leaf observation: %+v", plan.Leaf)
	}
	if zero {
		e.repair(l)
	} else {
		// The limits were set before the crash: repair is an idempotent success.
		e.exactRows(l)
		e.repair(l)
	}
	e.retire(l)
	if zero {
		t.Log("FRESH_RECOVERY_RESERVED_EMPTY_PROJECT_ZERO_PASS")
	} else {
		t.Log("FRESH_RECOVERY_RESERVED_EMPTY_ASSIGNED_PROJECT_PASS")
	}
}

// allocating (a): an empty root 0700 leaf at the recorded inode.
func (e *env) allocatingEmpty() {
	t := e.t
	l := e.newLeak("recovery-allocating-empty")
	e.advance(l, "allocating")
	_, err := freshrecovery.PlanRepair(t.Context(), l.manager, l.spec)
	refused(t, err, "repair in phase allocating")
	plan := e.removeLeaf(l)
	if len(plan.Leaf.Fixed) != 0 {
		t.Fatalf("allocating (a) plan has fixed names: %+v", plan.Leaf)
	}
	e.retire(l)
	t.Log("FRESH_RECOVERY_ALLOCATING_EMPTY_LEAF_PASS")
}

// allocating (b): a prefix of Begin's fixed directories, daemon-owned except
// the last, which is still root-owned because Begin crashed before its chown.
func (e *env) allocatingFixedPrefix() {
	t := e.t
	l := e.newLeak("recovery-allocating-fixed")
	e.advance(l, "allocating")
	fixed := payloadbootstrap.FixedDirectories(l.spec)[:4]
	for k, name := range fixed {
		path := filepath.Join(l.a.Path, name)
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if k < len(fixed)-1 {
			if err := os.Chown(path, e.p.OwnerUID, e.p.OwnerGID); err != nil {
				t.Fatal(err)
			}
		}
	}
	plan := e.removeLeaf(l)
	if len(plan.Leaf.Fixed) != len(fixed) {
		t.Fatalf("fixed prefix not planned: %+v", plan.Leaf.Fixed)
	}
	for k, entry := range plan.Leaf.Fixed {
		if entry.Name != fixed[k] || (k < len(fixed)-1) != (entry.Node.UID == uint32(e.p.OwnerUID)) {
			t.Fatalf("fixed entry %d: %+v", k, entry)
		}
	}
	e.retire(l)
	t.Log("FRESH_RECOVERY_ALLOCATING_FIXED_PREFIX_PASS")
}

// A leaf holding anything but fixed empty directories is refused at plan time
// and at effect time, before any approval is retained.
func (e *env) nonEmptyRefused() {
	t := e.t
	l := e.newLeak("recovery-nonempty")
	e.advance(l, "allocating")
	stray := filepath.Join(l.a.Path, ".superbench-git")
	if err := os.WriteFile(stray, []byte("not a fixed directory"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := freshrecovery.PlanLeaf(t.Context(), l.manager, l.spec)
	refused(t, err, "a leaf holding a file")
	if err = os.Remove(stray); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(l.a.Path, "stray"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err = freshrecovery.PlanLeaf(t.Context(), l.manager, l.spec)
	refused(t, err, "a leaf holding an unknown directory")
	if err = os.Remove(filepath.Join(l.a.Path, "stray")); err != nil {
		t.Fatal(err)
	}
	plan, err := freshrecovery.PlanLeaf(t.Context(), l.manager, l.spec)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(stray, []byte("added after review"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = freshrecovery.RemoveLeaf(t.Context(), l.manager, l.spec, plan)
	refused(t, err, "an effect on a leaf changed after review")
	if strings.Contains(err.Error(), "effects may have occurred") {
		t.Fatalf("refusal before retention reported effects: %v", err)
	}
	if _, err = os.Lstat(e.approval(l, "leaf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("approval retained for a refused leaf: %v", err)
	}
	assertBytes(t, stray, []byte("added after review"))
	if err = os.Remove(stray); err != nil {
		t.Fatal(err)
	}
	e.removeLeaf(l)
	e.retire(l)
	t.Log("FRESH_RECOVERY_NONEMPTY_LEAF_REFUSED_PASS")
}

// transferring stays parked: neither leaf removal nor repair accepts it.
func (e *env) wrongPhaseRefused() {
	t := e.t
	l := e.newLeak("recovery-wrong-phase")
	e.advance(l, "transferring")
	_, err := freshrecovery.PlanLeaf(t.Context(), l.manager, l.spec)
	refused(t, err, "leaf removal in phase transferring")
	_, err = freshrecovery.PlanRepair(t.Context(), l.manager, l.spec)
	refused(t, err, "repair in phase transferring")
	if _, err = os.Lstat(e.approval(l, "leaf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("approval retained for a parked phase: %v", err)
	}
	// Return to a recoverable phase so the fixture leaves no leak behind.
	e.advance(l, "allocating")
	e.removeLeaf(l)
	e.retire(l)
	t.Log("FRESH_RECOVERY_WRONG_PHASE_REFUSED_PASS")
}

// Existing rows with foreign hard caps or nonzero soft caps refuse repair, at
// plan time and at effect time before any approval is retained.
func (e *env) repairForeignCaps() {
	t := e.t
	l := e.newLeak("recovery-foreign-caps")
	if err := os.Remove(l.a.Path); err != nil {
		t.Fatal(err)
	}
	e.clearLimits(l)
	plan, err := freshrecovery.PlanRepair(t.Context(), l.manager, l.spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{
		fmt.Sprintf("limit -p bhard=1024k ihard=7 %d", l.a.ProjectID),
		fmt.Sprintf("limit -p bsoft=1024k bhard=%dk ihard=%d %d", l.a.Bytes/1024, l.a.Inodes, l.a.ProjectID),
	} {
		e.xfsQuota(command)
		_, err = freshrecovery.PlanRepair(t.Context(), l.manager, l.spec)
		refused(t, err, "repair plan over "+command)
		_, err = freshrecovery.Repair(t.Context(), l.manager, l.spec, plan)
		refused(t, err, "repair over "+command)
		if _, statErr := os.Lstat(e.approval(l, "repair")); !errors.Is(statErr, os.ErrNotExist) || strings.Contains(err.Error(), "effects may have occurred") {
			t.Fatalf("refused repair retained an approval: %v %v", statErr, err)
		}
		e.clearLimits(l)
	}
	e.repair(l)
	e.retire(l)
	t.Log("FRESH_RECOVERY_REPAIR_FOREIGN_CAPS_REFUSED_PASS")
}

func mustInfo(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func assertBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("retained bytes differ: %s %v", path, err)
	}
}
