//go:build linux && freshstoragefixture

package fixture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshrecovery"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

// The lower fixture's payload account (payloadexec.Policy in the parent).
const payloadUID, payloadGID = 61002, 61004

// bench is one completed Fresh bench made through the real manager Bootstrap.
type bench struct {
	spec       payloadbootstrap.Spec
	allocation hoststorage.Assignment
}

func (e *env) identity(label string) quotaoperation.Identity {
	i := e.template.Operation
	i.OperationID = hash("reclaim-operation:" + label)
	i.StateIdentity = hash("reclaim-state:" + label)
	return i
}

// bootstrap creates a completed Fresh bench under its own create identity:
// the same members as the parent's registered bench, a new key and BID.
func (e *env) bootstrap(key string) (bench, error) {
	s := e.template.Spec
	s.Key, s.ID, s.QuotaBytes = key, hash("reclaim-bid:" + key)[:32], 0
	m, err := payloadbootstrap.Open(e.t.Context(), e.control, e.identity("create:"+key))
	if err != nil {
		e.t.Fatal(err)
	}
	result, err := m.Bootstrap(e.t.Context(), s)
	return bench{s, result.Allocation}, err
}

// reclaimer opens the manager under the reclaim operation's own identity,
// derived as the daemon's transport derives it.
func (e *env) reclaimer(b bench) *payloadbootstrap.Manager {
	t := e.t
	i := quotahelper.ReclaimIdentity(b.spec.Key, b.spec.ID, e.template.Operation.Authority, e.template.Operation.BinarySHA256, hash("reclaim-state"))
	m, err := payloadbootstrap.Open(t.Context(), e.control, i)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (e *env) reclaim(ctx context.Context, b bench) (quotahelper.ReclaimResult, error) {
	return freshrecovery.Reclaim(ctx, e.reclaimer(b), b.spec)
}

// intentPhase reads the phase of the intent under stem, a generation's name.
func (e *env) intentPhase(stem string) string {
	e.t.Helper()
	raw, err := os.ReadFile(filepath.Join(e.p.Ledger, "payload-bootstrap", stem+".json"))
	if err != nil {
		e.t.Fatal(err)
	}
	var r record
	if err = json.Unmarshal(raw, &r); err != nil {
		e.t.Fatal(err)
	}
	return r.Phase
}

func (e *env) row(key string) hoststorage.Assignment {
	e.t.Helper()
	raw, err := os.ReadFile(filepath.Join(e.p.Ledger, "assignments.json"))
	if err != nil {
		e.t.Fatal(err)
	}
	var l struct {
		Assignments map[string]hoststorage.Assignment
	}
	if err = json.Unmarshal(raw, &l); err != nil {
		e.t.Fatal(err)
	}
	return l.Assignments["bench:"+key]
}

// payloadShell runs script as the payload account in the bench's root.
func (e *env) payloadShell(root, script string, args ...string) {
	t := e.t
	t.Helper()
	cmd := exec.Command("/bin/sh", append([]string{"-euc", script, "payload"}, args...)...)
	cmd.Dir = root
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "LC_ALL=C"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: payloadUID, Gid: payloadGID, Groups: []uint32{}}}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("payload write: %v %s", err, out)
	}
}

// hostileWrite leaves what a payload can: a symlink to a root-owned file of
// another bench and to another payload bench's file, a FIFO, a hard link,
// unreadable entries, a chain deeper than the worker's descriptor bound and a
// wide directory, under a member checkout and the root itself.
func (e *env) hostileWrite(b bench, files int) {
	victim := filepath.Join(e.p.Root, "benches", "registered", "api", "payload.txt")
	sentinel := filepath.Join(e.p.Root, "benches", "existing", "owned-sentinel")
	e.payloadShell(b.allocation.Path, `
mkdir -p api/out
printf data > api/out/file
ln -s "$1" api/out/escape-root
ln -s "$2" api/out/escape-payload
ln -s "$(dirname "$2")" api/out/escape-dir
ln -s . api/out/loop
mkfifo api/out/fifo
mkfifo root-fifo
ln api/out/file api/out/hard
d=api/out/deep; i=0
while [ "$i" -lt 200 ]; do d="$d/d"; i=$((i+1)); done
mkdir -p "$d"; printf x > "$d/f"
mkdir api/out/closed; printf x > api/out/closed/f; chmod 000 api/out/closed
chmod 000 api/out/file
i=0; while [ "$i" -lt "$3" ]; do printf x > "api/out/w$i"; i=$((i+1)); done
chmod 500 .
`, sentinel, victim, fmt.Sprint(files))
}

func (e *env) assertOutsideIntact() {
	e.t.Helper()
	assertBytes(e.t, filepath.Join(e.p.Root, "benches", "existing", "owned-sentinel"), []byte("existing retained"))
	victim := filepath.Join(e.p.Root, "benches", "registered", "api", "payload.txt")
	assertBytes(e.t, victim, []byte("source-api\n"))
	// A hard link the payload made to it, if XFS allowed one across
	// projects, went with the tree; the file itself keeps its one name.
	if st := mustInfo(e.t, victim).Sys().(*syscall.Stat_t); st.Nlink != 1 {
		e.t.Fatalf("outside file has %d links", st.Nlink)
	}
}

func (e *env) xfsIO(path, command string) {
	e.t.Helper()
	cmd := exec.Command("/usr/sbin/xfs_io", "-x", "-c", command, path)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	if out, err := cmd.CombinedOutput(); err != nil {
		e.t.Fatalf("xfs_io %q %s: %s %v", command, path, out, err)
	}
}

// inUse starts a payload process with its working directory in the bench,
// inside a populated scope beneath the policy's cgroup root, as a run is.
func (e *env) inUse(b bench) func() {
	t := e.t
	t.Helper()
	root := os.Getenv("SUPERBENCH_LOWER_CGROUP")
	scope := filepath.Join(root, hash("reclaim-in-use")[:32])
	if err := os.Mkdir(scope, 0755); err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Open(scope, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	cmd := exec.Command("/bin/sleep", "600")
	cmd.Dir = filepath.Join(b.allocation.Path, "api")
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: payloadUID, Gid: payloadGID, Groups: []uint32{}}, UseCgroupFD: true, CgroupFD: fd}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		deadline := time.Now().Add(10 * time.Second)
		for {
			raw, _ := os.ReadFile(filepath.Join(scope, "cgroup.events"))
			if strings.Contains(string(raw), "populated 0") || time.Now().After(deadline) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := os.Remove(scope); err != nil {
			t.Fatal(err)
		}
	}
}

// assertReclaimed requires the leaf gone, zero usage, the tombstone and the
// reclaimed intent.
func (e *env) assertReclaimed(b bench, got quotahelper.ReclaimResult) {
	t := e.t
	t.Helper()
	if _, err := os.Lstat(b.allocation.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("leaf remains: %v", err)
	}
	for _, unit := range []string{"-b", "-i"} {
		if f := strings.Fields(e.report(b.allocation.ProjectID, unit)); len(f) != 0 && (len(f) < 2 || f[1] != "0") {
			t.Fatalf("project %d %s usage after reclaim: %q", b.allocation.ProjectID, unit, f)
		}
	}
	tombstone := b.allocation
	tombstone.Bytes, tombstone.Inodes = 0, 0
	if row := e.row(b.spec.Key); row != tombstone || got.Allocation != tombstone || got.Key != b.spec.Key || got.Version != quotahelper.ReclaimVersion {
		t.Fatalf("project not retired: row %+v reply %+v", row, got)
	}
	if p := e.intentPhase(payloadbootstrap.Stem(b.spec)); p != "reclaimed" {
		t.Fatalf("intent phase %s", p)
	}
}

// reclaims proves a released bench's reclaim on real XFS: refusals before
// retention, a hostile tree through the sudo route, the lost-reply retry, the
// capacity it returns and a worker killed mid-purge.
func (e *env) reclaims() {
	t := e.t
	a, err := e.bootstrap("reclaim-a")
	if err != nil {
		t.Fatal(err)
	}
	// A foreign source vector and a legacy allocation refuse before retention.
	foreign := a
	foreign.spec.Members = append([]payloadbootstrap.Member{}, a.spec.Members...)
	foreign.spec.Members[0].Source = strings.Repeat("f", 40)
	if _, err = e.reclaim(t.Context(), foreign); err == nil || strings.Contains(err.Error(), "effects may have occurred") {
		t.Fatalf("foreign vector: %v", err)
	}
	legacy := bench{spec: a.spec}
	legacy.spec.Key, legacy.spec.ID = "existing", hash("reclaim-bid:existing")[:32]
	if _, err = e.reclaim(t.Context(), legacy); err == nil {
		t.Fatal("a legacy allocation reclaimed")
	}
	if p := e.intentPhase(a.spec.Key); p != "completed" {
		t.Fatalf("a refusal moved the intent to %s", p)
	}
	e.assertOutsideIntact()
	t.Log("FRESH_RECLAIM_REFUSED_BEFORE_RETENTION_PASS")

	// Root refuses a bench in use itself, whatever the daemon recorded.
	stop := e.inUse(a)
	if _, err = e.reclaim(t.Context(), a); !errors.Is(err, freshrecovery.ErrReclaimBusy) || strings.Contains(err.Error(), "effects may have occurred") {
		stop()
		t.Fatalf("a bench in use: %v", err)
	}
	stop()
	if p := e.intentPhase(a.spec.Key); p != "completed" {
		t.Fatalf("a refused busy reclaim moved the intent to %s", p)
	}
	assertBytes(t, filepath.Join(a.allocation.Path, "api", "payload.txt"), []byte("source-api\n"))
	t.Log("FRESH_RECLAIM_REFUSED_WHILE_IN_USE_PASS")

	// An owner's own chproj, made here outside the payload filter, is refused
	// by root's tree walk before retention; nothing changes, and root restores it.
	escaped := filepath.Join(a.allocation.Path, "api", "payload.txt")
	e.payloadShell(a.allocation.Path, `/usr/sbin/xfs_io -r -c 'chproj 0' api/payload.txt`)
	if got := e.attr(escaped); got.Projid != 0 {
		t.Fatalf("the unconfined owner's chproj did not apply: %+v", got)
	}
	e.refusedEscaped(a, escaped)
	fmt.Println("FRESH_RECLAIM_ESCAPED_FILE_REFUSED_PASS")
	e.xfsIO(escaped, fmt.Sprintf("chproj %d", a.allocation.ProjectID))
	api := filepath.Join(a.allocation.Path, "api")
	e.payloadShell(a.allocation.Path, `/usr/sbin/xfs_io -r -c 'chattr -P' api`)
	if got := e.attr(api); got.Xflags&projInherit != 0 {
		t.Fatalf("the unconfined owner's chattr -P did not apply: %+v", got)
	}
	e.refusedEscaped(a, escaped)
	fmt.Println("FRESH_RECLAIM_DIRECTORY_WITHOUT_INHERIT_REFUSED_PASS")
	restored := e.attr(api)
	restored.Xflags |= projInherit
	e.setAttr(api, restored)

	// The daemon's route is sudo: the reclaim admits it, as no recovery step does.
	e.hostileWrite(a, 50)
	// A hard link to a file outside the leaf: XFS refuses one across projects
	// (EXDEV), so root gives the outside file the bench's project first, which
	// a payload cannot do. Only that link goes with the tree; the file's usage,
	// still charged to the project, then blocks the tombstone.
	outside := filepath.Join(e.p.Root, "reclaim-same-project-outside")
	if err = os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chown(outside, payloadUID, payloadGID); err != nil {
		t.Fatal(err)
	}
	e.xfsIO(outside, fmt.Sprintf("chproj %d", a.allocation.ProjectID))
	e.payloadShell(a.allocation.Path, `chmod 700 . && ln "$1" api/out/hard-same-project && chmod 500 .`, outside)
	if st := mustInfo(t, outside).Sys().(*syscall.Stat_t); st.Nlink != 2 {
		t.Fatalf("same-project hard link not made: %d links", st.Nlink)
	}
	t.Setenv("SUDO_UID", "123")
	_, err = e.reclaim(t.Context(), a)
	t.Setenv("SUDO_UID", "")
	if err == nil || !strings.Contains(err.Error(), "effects may have occurred; retry the same reclaim") {
		t.Fatalf("project usage outside the leaf did not block the tombstone: %v", err)
	}
	if _, statErr := os.Lstat(a.allocation.Path); !errors.Is(statErr, os.ErrNotExist) || e.intentPhase(a.spec.Key) != "reclaiming" || e.row(a.spec.Key) != a.allocation {
		t.Fatalf("blocked tombstone left leaf %v, phase %s, row %+v", statErr, e.intentPhase(a.spec.Key), e.row(a.spec.Key))
	}
	assertBytes(t, outside, []byte("outside"))
	if st := mustInfo(t, outside).Sys().(*syscall.Stat_t); st.Nlink != 1 {
		t.Fatalf("outside file has %d links after the purge", st.Nlink)
	}
	t.Log("FRESH_RECLAIM_SAME_PROJECT_OUTSIDE_LINK_BLOCKS_RETIRE_PASS")
	e.xfsIO(outside, "chproj 0")
	t.Setenv("SUDO_UID", "123")
	got, err := e.reclaim(t.Context(), a)
	t.Setenv("SUDO_UID", "")
	if err != nil {
		t.Fatal("hostile reclaim:", err)
	}
	e.assertReclaimed(a, got)
	e.assertOutsideIntact()
	assertBytes(t, outside, []byte("outside"))
	if err = os.Remove(outside); err != nil {
		t.Fatal(err)
	}
	t.Log("FRESH_RECLAIM_HOSTILE_TREE_RETIRED_PASS")

	ledger, err := os.ReadFile(filepath.Join(e.p.Ledger, "assignments.json"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.reclaim(t.Context(), a)
	if err != nil || again != got {
		t.Fatalf("lost-reply retry: %+v %v", again, err)
	}
	assertBytes(t, filepath.Join(e.p.Ledger, "assignments.json"), ledger)
	t.Log("FRESH_RECLAIM_LOST_REPLY_RETRY_PASS")

	// Fill the volume, see a create refused for capacity, reclaim, create again.
	b, err := e.bootstrap("reclaim-b")
	if err != nil {
		t.Fatal(err)
	}
	var fillers []hoststorage.Assignment
	for i := 0; ; i++ {
		if i == 4096 {
			t.Fatal("the fixture volume never filled")
		}
		var f hoststorage.Assignment
		err = e.control.Run(t.Context(), func() error {
			var e2 error
			f, e2 = e.p.ApplyRequested("bench", fmt.Sprintf("reclaim-fill-%d", i), e.p.BenchBytes)
			return e2
		})
		var capacity *hoststorage.CapacityError
		if errors.As(err, &capacity) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		fillers = append(fillers, f)
	}
	var capacity *hoststorage.CapacityError
	if _, err = e.bootstrap("reclaim-c"); !errors.As(err, &capacity) {
		t.Fatalf("a create on a full volume: %v", err)
	}
	t.Logf("create refused: %v", err)
	got, err = e.reclaim(t.Context(), b)
	if err != nil {
		t.Fatal(err)
	}
	e.assertReclaimed(b, got)
	c, err := e.bootstrap("reclaim-c")
	if err != nil {
		t.Fatal("the create after reclaim:", err)
	}
	t.Log("FRESH_RECLAIM_CAPACITY_RETURNED_PASS")

	// A worker killed mid-purge: the intent stays reclaiming, the error says
	// effects may have occurred, and the same reclaim finishes.
	files := 3000
	e.hostileWrite(c, files)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := e.reclaim(ctx, c); done <- err }()
	out := filepath.Join(c.allocation.Path, "api", "out")
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if entries, statErr := os.ReadDir(out); statErr == nil && len(entries) < files {
			// The purge worker, mid-purge, is under the payload filter.
			if confinedErr := e.confinedPurge(); confinedErr != nil {
				cancel()
				<-done
				t.Fatal(confinedErr)
			}
			fmt.Println("FRESH_RECLAIM_PURGE_WORKER_CONFINED_PASS")
			cancel()
			err = <-done
			break
		}
		select {
		case err = <-done:
			t.Fatalf("reclaim finished before it could be killed: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("the purge never started")
		}
		time.Sleep(time.Millisecond)
	}
	if err == nil || !strings.Contains(err.Error(), "effects may have occurred; retry the same reclaim") {
		t.Fatalf("killed reclaim: %v", err)
	}
	if p := e.intentPhase(c.spec.Key); p != "reclaiming" {
		t.Fatalf("killed reclaim left phase %s", p)
	}
	if _, err = os.Lstat(c.allocation.Path); err != nil {
		t.Fatalf("the leaf went with the killed worker: %v", err)
	}
	if row := e.row(c.spec.Key); row != c.allocation {
		t.Fatalf("killed reclaim retired early: %+v", row)
	}
	got, err = e.reclaim(t.Context(), c)
	if err != nil {
		t.Fatal("retry after the kill:", err)
	}
	e.assertReclaimed(c, got)
	e.assertOutsideIntact()
	t.Log("FRESH_RECLAIM_KILLED_WORKER_RETRY_PASS")

	// Return the fillers so the parent's later steps see the volume as before.
	for _, f := range fillers {
		if err = e.control.Run(t.Context(), func() error { _, e2 := e.p.Release(filepath.Base(f.Path), f.ProjectID); return e2 }); err != nil {
			t.Fatal(err)
		}
	}
}
