//go:build linux && freshstoragefixture

package fixture

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/cleanworker"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshcleanup"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshrecovery"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/retainedfile"
)

// The recovery child is this test binary, so the cleanup steps re-execute it,
// as /proc/self/exe, for the daemon-uid clean worker. Dispatch the worker here,
// before the testing package parses flags, exactly as bench-quota's main does.
// The test binary needs //go:debug containermaxprocs=0 for the worker's
// descriptor check, as bench-quota has.
func init() {
	if len(os.Args) > 1 && os.Args[1] == cleanworker.Command {
		os.Exit(cleanworker.Main(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
}

// cleanupLeaks is the number of leaks the cleanup cases retire.
const cleanupLeaks = 8

// cleanups runs every daemon-owned cleanup case, each to a retired tombstone.
func (e *env) cleanups() {
	e.stagedGitTrees()
	e.allocatingC()
	e.changedEntryRefused()
	e.symlinkSwapRefused()
	e.unreadableRefused()
	e.workerKilled()
	e.crashAfterRetention()
	e.transferringParked()
}

// controls creates KEY.controls as Begin does: a root-owned private directory
// whose node the intent records. Its git-controls journal is absent.
func (e *env) controls(l *leak) node {
	e.t.Helper()
	path := filepath.Join(e.p.Ledger, "payload-bootstrap", l.spec.Key+".controls")
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		e.t.Fatal(err)
	}
	st := mustInfo(e.t, path).Sys().(*syscall.Stat_t)
	return node{uint64(st.Dev), st.Ino}
}

// advanceDaemon rewrites the leak's intent to phase with a real KEY.controls,
// as Begin records both before it hands the leaf to the daemon.
func (e *env) advanceDaemon(l *leak, phase string) {
	e.t.Helper()
	st := mustInfo(e.t, l.a.Path).Sys().(*syscall.Stat_t)
	l.intent.Phase, l.intent.Allocation = phase, l.a
	l.intent.Root = node{uint64(st.Dev), st.Ino}
	l.intent.Controls = e.controls(l)
	if err := e.writeIntent(l.intent, false); err != nil {
		e.t.Fatal(err)
	}
	if got, err := l.manager.Inspect(e.t.Context(), l.spec); err != nil || got.Phase != phase || !got.OperationDenied {
		e.t.Fatalf("synthetic %s intent unreadable: %+v %v", phase, got, err)
	}
}

// own makes path the daemon's, as the daemon's own writes would leave it.
func (e *env) own(path string) {
	e.t.Helper()
	if err := os.Lchown(path, e.p.OwnerUID, e.p.OwnerGID); err != nil {
		e.t.Fatal(err)
	}
}

// daemonTree gives the leaf to the daemon with Begin's fixed directories and a
// small working tree, all daemon-owned, as a crash after Begin leaves it.
func (e *env) daemonTree(l *leak, files int, size int) {
	t := e.t
	t.Helper()
	for _, name := range payloadbootstrap.FixedDirectories(l.spec) {
		path := filepath.Join(l.a.Path, name)
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		e.own(path)
	}
	work := filepath.Join(l.a.Path, "work")
	if err := os.Mkdir(work, 0755); err != nil {
		t.Fatal(err)
	}
	e.own(work)
	for k := range files {
		path := filepath.Join(work, fmt.Sprintf("f%04d", k))
		if err := os.WriteFile(path, bytes.Repeat([]byte{byte('a' + k%26)}, size), 0644); err != nil {
			t.Fatal(err)
		}
		e.own(path)
	}
	link := filepath.Join(work, "link")
	if err := os.Symlink("f0000", link); err != nil {
		t.Fatal(err)
	}
	e.own(link)
	e.own(l.a.Path)
}

// syntheticLeak is a daemon-owned leak in phase with a working tree.
func (e *env) syntheticLeak(key, phase string, files, size int) *leak {
	l := e.newLeak(key)
	e.daemonTree(l, files, size)
	e.advanceDaemon(l, phase)
	return l
}

func (e *env) planFile(l *leak) string { return e.approval(l, "plan") }
func (e *env) approvalFile(l *leak) string {
	return filepath.Join(e.p.Ledger, "payload-cleanups", l.spec.Key+".json")
}

// planClean plans the cleanup and checks the published plan: its path, its
// digest, the daemon owner and the assignment, and that nothing is retained.
func (e *env) planClean(l *leak) freshrecovery.PlanFile {
	t := e.t
	t.Helper()
	plan, err := freshrecovery.PlanClean(t.Context(), l.manager, l.spec)
	if err != nil {
		t.Fatalf("cleanup plan refused: %v", err)
	}
	raw, err := os.ReadFile(plan.Path)
	if err != nil || plan.Path != e.planFile(l) || plan.SHA256 != hash(string(raw)) {
		t.Fatalf("published plan differs from its report: %+v %v", plan, err)
	}
	var a freshcleanup.Approval
	if err = json.Unmarshal(raw, &a); err != nil {
		t.Fatal(err)
	}
	if a.Owner != (freshcleanup.Owner{UID: uint32(e.p.OwnerUID), GID: uint32(e.p.OwnerGID)}) || a.Assignment != (freshcleanup.Assignment{ProjectID: l.a.ProjectID, Bytes: l.a.Bytes, Inodes: l.a.Inodes}) || a.RootPath != l.a.Path {
		t.Fatalf("plan binds another owner or assignment: %+v %+v %s", a.Owner, a.Assignment, a.RootPath)
	}
	e.noApproval(l)
	e.cleanRequest(l, plan.SHA256)
	return plan
}

func (e *env) approvalOf(path string) freshcleanup.Approval {
	e.t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		e.t.Fatal(err)
	}
	var a freshcleanup.Approval
	if err = json.Unmarshal(raw, &a); err != nil {
		e.t.Fatal(err)
	}
	return a
}

// cleanRequest round-trips the command's canonical {Spec, PlanSHA256} input.
func (e *env) cleanRequest(l *leak, sha string) {
	e.t.Helper()
	raw, err := json.Marshal(struct {
		Spec       payloadbootstrap.Spec
		PlanSHA256 string
	}{l.spec, sha})
	if err != nil {
		e.t.Fatal(err)
	}
	if s, got, err := freshrecovery.ParseClean(raw); err != nil || s.Key != l.spec.Key || got != sha {
		e.t.Fatalf("reviewed clean request does not parse: %v", err)
	}
}

func (e *env) noApproval(l *leak) {
	e.t.Helper()
	if _, err := os.Lstat(e.approvalFile(l)); !errors.Is(err, os.ErrNotExist) {
		e.t.Fatalf("cleanup approval retained: %v", err)
	}
}

// emptyLeaf requires the leaf to be the recorded daemon-owned private node,
// still present and holding nothing.
func (e *env) emptyLeaf(l *leak) {
	t := e.t
	t.Helper()
	info := mustInfo(t, l.a.Path)
	st := info.Sys().(*syscall.Stat_t)
	names, err := os.ReadDir(l.a.Path)
	if err != nil || len(names) != 0 || st.Ino != l.intent.Root.Inode || st.Uid != uint32(e.p.OwnerUID) || info.Mode() != os.ModeDir|0700 {
		t.Fatalf("leaf not left empty and unchanged: %d entries %+v %v", len(names), info.Mode(), err)
	}
}

// clean runs the reviewed cleanup, checks the gate, the retained approval and
// the empty leaf, then retries it as after a lost reply.
func (e *env) clean(l *leak, plan freshrecovery.PlanFile) freshrecovery.Result {
	t := e.t
	t.Helper()
	before := *l.gateHits
	got, err := freshrecovery.Clean(t.Context(), l.manager, l.spec, plan.SHA256)
	if err != nil || got.ApprovalSHA256 != plan.SHA256 || got.Key != l.spec.Key || got.Step != "clean-bootstrap" {
		t.Fatalf("cleanup failed: %+v %v", got, err)
	}
	if *l.gateHits < before+4 {
		t.Fatalf("cleanup skipped the maintenance gate: %d", *l.gateHits-before)
	}
	planned, err := os.ReadFile(plan.Path)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, e.approvalFile(l), planned)
	e.emptyLeaf(l)
	e.noDaemonProcess()
	again, err := freshrecovery.Clean(t.Context(), l.manager, l.spec, plan.SHA256)
	if err != nil || again != got {
		t.Fatalf("lost-reply cleanup retry: %+v %v", again, err)
	}
	e.emptyLeaf(l)
	return got
}

// finish removes the empty leaf, retries the cleanup with the leaf gone, and
// retires the reservation.
func (e *env) finish(l *leak, plan freshrecovery.PlanFile, want freshrecovery.Result) {
	t := e.t
	t.Helper()
	leaf := e.removeLeaf(l)
	if leaf.Leaf.Node.UID != uint32(e.p.OwnerUID) || len(leaf.Leaf.Fixed) != 0 {
		t.Fatalf("cleaned leaf plan: %+v", leaf.Leaf)
	}
	again, err := freshrecovery.Clean(t.Context(), l.manager, l.spec, plan.SHA256)
	if err != nil || again != want {
		t.Fatalf("cleanup retry after the leaf is gone: %+v %v", again, err)
	}
	e.retire(l)
	if _, err = freshrecovery.PlanClean(t.Context(), l.manager, l.spec); err == nil {
		t.Fatal("retired allocation selected for cleanup")
	}
}

// recover takes a leak through plan, clean, leaf removal and retirement.
func (e *env) recoverDaemon(l *leak) {
	e.t.Helper()
	plan := e.planClean(l)
	got := e.clean(l, plan)
	e.finish(l, plan, got)
}

// isolatedWorker waits, up to a few seconds, until the one live daemon-uid
// process, the worker, is seen after its self-checks: leading its own session
// with no controlling tty, non-dumpable (procfs then shows its per-process
// files, such as mem, as root's), untraced, in a transient worker cgroup, with no_new_privs, an empty
// bounding set and no groups. It is called once the worker has visibly started
// deleting, and the procfs view is re-read until it settles. It returns the
// last refusal instead of failing, so the caller can stop the cleanup and let
// it remove its cgroup before the test fails.
func (e *env) isolatedWorker() error {
	// Hold the worker mid-delete while it is inspected; the caller's cancel
	// then kills its cgroup, which takes a stopped process too.
	_ = e.checkWorker(syscall.SIGSTOP)
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := e.checkWorker(0)
		if err == nil || time.Now().After(deadline) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// checkWorker checks every daemon-uid process; a nonzero signal is sent to
// each first.
func (e *env) checkWorker(signal syscall.Signal) error {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}
	found := 0
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		dir := filepath.Join("/proc", entry.Name())
		status, err := os.ReadFile(filepath.Join(dir, "status"))
		if err != nil || !strings.Contains(string(status), "\nUid:\t"+strconv.Itoa(e.p.OwnerUID)+"\t") {
			continue
		}
		found++
		if signal != 0 {
			_ = syscall.Kill(pid, signal)
			continue
		}
		cmdline, _ := os.ReadFile(filepath.Join(dir, "cmdline"))
		stat, err := os.ReadFile(filepath.Join(dir, "stat"))
		if err != nil {
			return err
		}
		f := strings.Fields(string(stat[bytes.LastIndexByte(stat, ')')+1:]))
		if len(f) < 5 || f[3] != strconv.Itoa(pid) || f[4] != "0" {
			return fmt.Errorf("worker %d shares a session or has a tty: %q", pid, stat)
		}
		// A non-dumpable process's per-process files are root's. The
		// /proc/PID directory itself keeps the task's owner on current
		// kernels, so it is not the signal.
		for _, file := range []string{"mem", "environ", "fd"} {
			info, err := os.Lstat(filepath.Join(dir, file))
			if err != nil {
				return err
			}
			if st := info.Sys().(*syscall.Stat_t); st.Uid != 0 || st.Gid != 0 {
				return fmt.Errorf("worker %d (%q) is dumpable: /proc/%d/%s owned by %d:%d; status %s", pid, cmdline, pid, file, st.Uid, st.Gid, status)
			}
		}
		cgroup, err := os.ReadFile(filepath.Join(dir, "cgroup"))
		if err != nil || !strings.Contains(string(cgroup), "/superbench-clean-") {
			return fmt.Errorf("worker %d outside a worker cgroup: %q %v", pid, cgroup, err)
		}
		for _, want := range []string{"\nTracerPid:\t0\n", "\nNoNewPrivs:\t1\n", "\nCapBnd:\t0000000000000000\n", "\nGroups:\t\n"} {
			if !strings.Contains(string(status), want) {
				return fmt.Errorf("worker %d status lacks %q: %s", pid, want, status)
			}
		}
	}
	if found != 1 {
		return fmt.Errorf("%d daemon-uid processes during the delete, want the worker alone", found)
	}
	return nil
}

// staleScope leaves an empty worker cgroup as a crash would, beneath the
// policy's cgroup root; the next worker run must sweep it.
func (e *env) staleScope() string {
	e.t.Helper()
	p, err := payloadexec.LoadFixedPolicy()
	if err != nil {
		e.t.Fatal(err)
	}
	path := filepath.Join(p.CgroupRoot, "superbench-clean-"+hash("stale-scope")[:32])
	if err = os.Mkdir(path, 0700); err != nil {
		e.t.Fatal(err)
	}
	return path
}

// noDaemonProcess requires that no daemon-uid process survived a worker.
func (e *env) noDaemonProcess() {
	t := e.t
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "status"))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if f := strings.Fields(line); len(f) == 5 && f[0] == "Uid:" && f[1] == strconv.Itoa(e.p.OwnerUID) {
				t.Fatalf("daemon-uid process %s outlived its worker", entry.Name())
			}
		}
	}
}

// refusedBeforeRetention requires a cleanup refusal with no approval retained
// and no claim of effects.
func (e *env) refusedBeforeRetention(l *leak, sha, what string) {
	e.t.Helper()
	_, err := freshrecovery.Clean(e.t.Context(), l.manager, l.spec, sha)
	refused(e.t, err, what)
	if strings.Contains(err.Error(), "effects may have occurred") {
		e.t.Fatalf("%s: refusal before retention reported effects: %v", what, err)
	}
	e.noApproval(l)
}

// retainAsCrashed publishes KEY.json from the reviewed plan exactly as Clean's
// first effect does, leaving the state of a crash right after retention.
func (e *env) retainAsCrashed(l *leak, plan freshrecovery.PlanFile) {
	e.t.Helper()
	raw, err := os.ReadFile(plan.Path)
	if err != nil {
		e.t.Fatal(err)
	}
	if err = retainedfile.Publish(filepath.Dir(plan.Path), l.spec.Key+".json", raw, freshcleanup.MaxApproval); err != nil {
		e.t.Fatal(err)
	}
}

// A staged leak with real Git trees: the actual Begin and Materialize ran
// under an admitted operation, which was then cancelled.
func (e *env) stagedGitTrees() {
	t := e.t
	s := e.template.Spec
	s.Key, s.ID, s.QuotaBytes = "cleanup-staged", hash("cleanup-bid:staged")[:32], 16<<20
	identity := e.template.Operation
	identity.OperationID = hash("cleanup-operation:staged")
	identity.StateIdentity = hash("cleanup-state:staged")
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
	m, err := payloadbootstrap.Open(t.Context(), e.control, identity)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Begin(t.Context(), s); err != nil {
		t.Fatalf("actual Begin: %v", err)
	}
	if err = m.Materialize(t.Context(), s); err != nil {
		t.Fatalf("actual Materialize: %v", err)
	}
	if _, err = store.Cancel(t.Context()); err != nil {
		t.Fatal(err)
	}
	i, err := m.Inspect(t.Context(), s)
	if err != nil || i.Phase != "staged" || !i.OperationDenied {
		t.Fatalf("materialized leak: %+v %v", i, err)
	}
	l := &leak{spec: s, manager: m, a: hoststorage.Assignment(i.RecordedAllocation), gateHits: new(int)}
	l.intent.Root = node{i.RecordedRoot.Device, i.RecordedRoot.Inode}
	hits := l.gateHits
	e.gate(m, func(context.Context, hoststorage.Policy) error { *hits++; return nil })
	for _, member := range s.Members {
		if _, err := os.Lstat(filepath.Join(l.a.Path, member.Name, ".git")); err != nil {
			t.Fatalf("no Git tree for %s: %v", member.Name, err)
		}
	}
	stale := e.staleScope()
	plan := e.planClean(l)
	if _, err := os.Lstat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an empty stale worker cgroup was not swept: %v", err)
	}
	a := e.approvalOf(plan.Path)
	git := 0
	for _, entry := range a.Observation.Tree.Entries {
		if strings.Contains(entry.Path, "/.git/") || strings.HasPrefix(entry.Path, ".superbench-git/") {
			git++
		}
	}
	if git == 0 || len(a.Observation.Tree.Entries) < 20 {
		t.Fatalf("plan lacks the Git trees: %d of %d entries", git, len(a.Observation.Tree.Entries))
	}
	got := e.clean(l, plan)
	e.finish(l, plan, got)
	t.Log("FRESH_CLEANUP_STAGED_GIT_TREES_PASS")
}

// allocating (c): Begin created every fixed directory and handed the leaf to
// the daemon, then crashed before it recorded begun.
func (e *env) allocatingC() {
	t := e.t
	l := e.newLeak("cleanup-allocating-c")
	for _, name := range payloadbootstrap.FixedDirectories(l.spec) {
		path := filepath.Join(l.a.Path, name)
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		e.own(path)
	}
	e.own(l.a.Path)
	e.advanceDaemon(l, "allocating")
	// Root's fixed-name removal does not take a daemon-owned tree.
	_, err := freshrecovery.PlanLeaf(t.Context(), l.manager, l.spec)
	refused(t, err, "leaf removal of a daemon-owned non-empty leaf")
	e.recoverDaemon(l)
	t.Log("FRESH_CLEANUP_ALLOCATING_C_PASS")
}

// An entry added or changed after review refuses the cleanup before anything
// is retained. After retention (a crash there), the worker's delete refuses a
// new entry before deleting anything.
func (e *env) changedEntryRefused() {
	t := e.t
	l := e.syntheticLeak("cleanup-changed", "begun", 4, 64)
	plan := e.planClean(l)
	added := filepath.Join(l.a.Path, "work", "added")
	if err := os.WriteFile(added, []byte("added after review"), 0644); err != nil {
		t.Fatal(err)
	}
	e.own(added)
	e.refusedBeforeRetention(l, plan.SHA256, "an entry added after review")
	if err := os.Remove(added); err != nil {
		t.Fatal(err)
	}
	changed := filepath.Join(l.a.Path, "work", "f0001")
	original, err := os.ReadFile(changed)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(changed, []byte("changed after review"), 0644); err != nil {
		t.Fatal(err)
	}
	e.refusedBeforeRetention(l, plan.SHA256, "an entry changed after review")
	if err = os.WriteFile(changed, original, 0644); err != nil {
		t.Fatal(err)
	}
	// A crash right after retention, then a new entry: nothing approved goes.
	e.retainAsCrashed(l, plan)
	if _, err = freshrecovery.PlanClean(t.Context(), l.manager, l.spec); err == nil {
		t.Fatal("plan replaced after its approval was retained")
	}
	if err = os.WriteFile(added, []byte("added after retention"), 0644); err != nil {
		t.Fatal(err)
	}
	e.own(added)
	_, err = freshrecovery.Clean(t.Context(), l.manager, l.spec, plan.SHA256)
	if err == nil || !strings.Contains(err.Error(), "effects may have occurred") {
		t.Fatalf("cleanup over a new entry: %v", err)
	}
	a := e.approvalOf(plan.Path)
	for _, entry := range a.Observation.Tree.Entries {
		if _, err := os.Lstat(filepath.Join(l.a.Path, entry.Path)); err != nil {
			t.Fatalf("approved %s deleted beside an unapproved entry: %v", entry.Path, err)
		}
	}
	assertBytes(t, added, []byte("added after retention"))
	e.noDaemonProcess()
	if err = os.Remove(added); err != nil {
		t.Fatal(err)
	}
	got := e.clean(l, plan)
	e.finish(l, plan, got)
	t.Log("FRESH_CLEANUP_CHANGED_ENTRY_REFUSED_PASS")
}

// A directory swapped for a symlink to a daemon-owned tree outside the leaf is
// refused before retention, and after retention by the worker's delete; the
// outside tree and every approved entry stay.
func (e *env) symlinkSwapRefused() {
	t := e.t
	l := e.syntheticLeak("cleanup-symlink", "staging", 3, 64)
	// The fixture's own base directory, beside the data mount: outside the leaf
	// and outside the data root's layout.
	outside := filepath.Join(filepath.Dir(e.p.Root), "cleanup-outside")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	e.own(outside)
	sentinel := filepath.Join(outside, "f0000")
	if err := os.WriteFile(sentinel, []byte("outside the leaf"), 0644); err != nil {
		t.Fatal(err)
	}
	e.own(sentinel)
	plan := e.planClean(l)
	work, moved := filepath.Join(l.a.Path, "work"), filepath.Join(l.a.Path, "work-moved")
	swap := func() {
		if err := os.Rename(work, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, work); err != nil {
			t.Fatal(err)
		}
		e.own(work)
	}
	restore := func() {
		if err := os.Remove(work); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(moved, work); err != nil {
			t.Fatal(err)
		}
	}
	swap()
	e.refusedBeforeRetention(l, plan.SHA256, "a symlink swapped in after review")
	assertBytes(t, sentinel, []byte("outside the leaf"))
	restore()
	e.retainAsCrashed(l, plan)
	swap()
	_, err := freshrecovery.Clean(t.Context(), l.manager, l.spec, plan.SHA256)
	if err == nil || !strings.Contains(err.Error(), "effects may have occurred") {
		t.Fatalf("cleanup through a swapped symlink: %v", err)
	}
	assertBytes(t, sentinel, []byte("outside the leaf"))
	if _, err = os.Lstat(filepath.Join(moved, "f0002")); err != nil {
		t.Fatalf("approved entry deleted through the swap: %v", err)
	}
	e.noDaemonProcess()
	restore()
	got := e.clean(l, plan)
	assertBytes(t, sentinel, []byte("outside the leaf"))
	e.finish(l, plan, got)
	if err = os.Remove(sentinel); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(outside); err != nil {
		t.Fatal(err)
	}
	t.Log("FRESH_CLEANUP_SYMLINK_SWAP_REFUSED_PASS")
}

// A file the daemon cannot read or a directory it cannot search fails the
// worker's observation; a directory it cannot write fails the model's owner
// rwx rule. Each is refused at plan time with no plan and no approval.
func (e *env) unreadableRefused() {
	t := e.t
	l := e.syntheticLeak("cleanup-unreadable", "materialized_unfenced", 3, 64)
	for name, c := range map[string]struct {
		path string
		mode os.FileMode
	}{
		"an unreadable file":                 {filepath.Join(l.a.Path, "work", "f0001"), 0200},
		"a directory without search":         {filepath.Join(l.a.Path, "work"), 0600},
		"a directory without owner write":    {filepath.Join(l.a.Path, "work"), 0500},
		"a fixed directory without search":   {filepath.Join(l.a.Path, ".superbench-git"), 0600},
		"a read-only fixed directory parent": {filepath.Join(l.a.Path, ".superbench-runtime"), 0500},
	} {
		original := mustInfo(t, c.path).Mode().Perm()
		if err := os.Chmod(c.path, c.mode); err != nil {
			t.Fatal(err)
		}
		_, err := freshrecovery.PlanClean(t.Context(), l.manager, l.spec)
		if err == nil {
			t.Fatalf("%s planned", name)
		}
		if _, err = os.Lstat(e.planFile(l)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s published a plan: %v", name, err)
		}
		e.noApproval(l)
		e.noDaemonProcess()
		if err = os.Chmod(c.path, original); err != nil {
			t.Fatal(err)
		}
	}
	e.recoverDaemon(l)
	t.Log("FRESH_CLEANUP_UNREADABLE_REFUSED_PASS")
}

// The worker is killed mid-delete (its context is cancelled, so root kills
// its cgroup). Approved entries are partly gone, nothing else is touched, no
// daemon process survives, and the same plan then completes.
func (e *env) workerKilled() {
	t := e.t
	l := e.newLeakSized("cleanup-killed", 12<<20)
	files := 2000
	e.daemonTree(l, files, 4<<10)
	e.advanceDaemon(l, "staged")
	plan := e.planClean(l)
	last := filepath.Join(l.a.Path, "work", fmt.Sprintf("f%04d", files-1))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := freshrecovery.Clean(ctx, l.manager, l.spec, plan.SHA256); done <- err }()
	var err error
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if _, statErr := os.Lstat(last); errors.Is(statErr, os.ErrNotExist) {
			// The real worker, mid-delete: it must have passed its own checks.
			workerErr := e.isolatedWorker()
			cancel()
			if workerErr != nil {
				<-done
				t.Fatal(workerErr)
			}
			err = <-done
			break
		}
		select {
		case err = <-done:
			t.Fatalf("cleanup finished before it could be killed: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("deletion never started")
		}
		time.Sleep(time.Millisecond)
	}
	if err == nil || !strings.Contains(err.Error(), "effects may have occurred") {
		t.Fatalf("killed cleanup: %v", err)
	}
	remaining, err := os.ReadDir(filepath.Join(l.a.Path, "work"))
	if err != nil || len(remaining) == 0 || len(remaining) >= files+1 {
		t.Fatalf("worker was not killed mid-delete: %d of %d remain %v", len(remaining), files+1, err)
	}
	if _, err = os.Lstat(e.approvalFile(l)); err != nil {
		t.Fatalf("approval not retained before the delete: %v", err)
	}
	e.noDaemonProcess()
	got := e.clean(l, plan)
	e.finish(l, plan, got)
	t.Log("FRESH_CLEANUP_WORKER_KILLED_RETRY_PASS")
}

// A crash right after retention: KEY.json exists and nothing was deleted. The
// plan can no longer be replaced, and the same plan then completes.
func (e *env) crashAfterRetention() {
	t := e.t
	l := e.syntheticLeak("cleanup-crash", "begun", 5, 64)
	plan := e.planClean(l)
	planned, err := os.ReadFile(plan.Path)
	if err != nil {
		t.Fatal(err)
	}
	e.retainAsCrashed(l, plan)
	if _, err = freshrecovery.PlanClean(t.Context(), l.manager, l.spec); err == nil {
		t.Fatal("plan replaced after its approval was retained")
	}
	assertBytes(t, plan.Path, planned)
	other := strings.Repeat("0", 64)
	_, err = freshrecovery.Clean(t.Context(), l.manager, l.spec, other)
	refused(t, err, "a cleanup under another plan digest")
	if _, err = os.Lstat(filepath.Join(l.a.Path, "work", "f0004")); err != nil {
		t.Fatalf("a refused digest deleted: %v", err)
	}
	got := e.clean(l, plan)
	e.finish(l, plan, got)
	t.Log("FRESH_CLEANUP_CRASH_AFTER_RETENTION_RETRY_PASS")
}

// transferring holds a mixed tree and stays parked: no plan, no approval and
// no deletion. The fixture then returns it to staged so it leaves no leak.
func (e *env) transferringParked() {
	t := e.t
	l := e.syntheticLeak("cleanup-transferring", "staged", 3, 64)
	plan := e.planClean(l)
	if err := os.Remove(plan.Path); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(l.a.Path, "work", "f0002")
	if err := os.Lchown(payload, e.p.OwnerUID+1, e.p.OwnerGID+1); err != nil {
		t.Fatal(err)
	}
	e.advanceDaemon(l, "transferring")
	_, err := freshrecovery.PlanClean(t.Context(), l.manager, l.spec)
	refused(t, err, "cleanup plan in phase transferring")
	if _, err = os.Lstat(e.planFile(l)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plan published for a parked phase: %v", err)
	}
	e.refusedBeforeRetention(l, plan.SHA256, "cleanup in phase transferring")
	if _, err = os.Lstat(payload); err != nil {
		t.Fatalf("parked tree changed: %v", err)
	}
	e.own(payload)
	e.advanceDaemon(l, "staged")
	e.recoverDaemon(l)
	t.Log("FRESH_CLEANUP_TRANSFERRING_PARKED_PASS")
}
