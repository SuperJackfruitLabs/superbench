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

func (e *env) intentPhase(key string) string {
	e.t.Helper()
	raw, err := os.ReadFile(filepath.Join(e.p.Ledger, "payload-bootstrap", key+".json"))
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
	assertBytes(e.t, filepath.Join(e.p.Root, "benches", "registered", "api", "payload.txt"), []byte("source-api\n"))
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
	if p := e.intentPhase(b.spec.Key); p != "reclaimed" {
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

	// The daemon's route is sudo: the reclaim admits it, as no recovery step does.
	e.hostileWrite(a, 50)
	t.Setenv("SUDO_UID", "123")
	got, err := e.reclaim(t.Context(), a)
	t.Setenv("SUDO_UID", "")
	if err != nil {
		t.Fatal("hostile reclaim:", err)
	}
	e.assertReclaimed(a, got)
	e.assertOutsideIntact()
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
