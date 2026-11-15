package workspaceflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

// This test requires the disposable XFS/cgroup wrapper. Ordinary CI must never
// configure a machine's actual host policy, mount storage, or acquire root.
func TestManagedWorkspaceAggregateQuota(t *testing.T) {
	trial := os.Getenv("SUPERBENCH_WORKSPACE_MANAGED_TRIAL")
	if trial == "" {
		t.Skip("opt-in disposable XFS drill; use scripts/test-workspace-staging.sh")
	}
	if runtime.GOOS != "linux" || os.Getuid() != 0 || os.Geteuid() != 0 || os.Getenv("SUDO_UID") != "" {
		t.Fatal("managed drill requires direct root Linux administration")
	}
	if !strings.HasPrefix(trial, "/var/tmp/superbench-workspace.") || filepath.Clean(trial) != trial {
		t.Fatal("invalid disposable workspace trial path")
	}
	helper := os.Getenv("SUPERBENCH_WORKSPACE_QUOTA_HELPER")
	cgroups := os.Getenv("SUPERBENCH_WORKSPACE_CGROUP_ROOT")
	if !filepath.IsAbs(helper) || !strings.HasPrefix(cgroups, "/sys/fs/cgroup/superbench-workspace-") {
		t.Fatal("managed fixture paths missing")
	}
	data := filepath.Join(trial, "data")
	state := filepath.Join(trial, "state")
	ledger := filepath.Join(trial, "quota-ledger")
	policyPath := filepath.Join(trial, "policy.json")
	const total = uint64(32 << 20)
	policy := hoststorage.Policy{Root: data, Ledger: ledger, OwnerUID: 0, OwnerGID: 0, BenchBytes: 64 << 20, StoreBytes: 32 << 20, MirrorBytes: 64 << 20, ReserveBytes: 64 << 20, BenchInodes: 4096, StoreInodes: 8192, MirrorInodes: 8192, ReserveInodes: 1024}
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(policyPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	store := bench.Store{State: state, Data: data, CgroupRoot: cgroups}
	if err = store.ConfigureHost(bench.HostConfig{DataRoot: data, QuotaHelper: helper, PolicyOverride: policyPath, MinDataFreeBytes: 8 << 20, MinStateFreeBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	type allocationLedger struct {
		Next        uint32
		Assignments map[string]hoststorage.Assignment
	}
	ledgerPath := filepath.Join(ledger, "assignments.json")
	var prior allocationLedger
	if raw, readErr := os.ReadFile(ledgerPath); readErr == nil {
		if err := json.Unmarshal(raw, &prior); err != nil {
			t.Fatal(err)
		}
	} else if !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	spec := workspace.Spec{Key: "card", CheckpointRepository: "a", QuotaBytes: total}
	for _, name := range []string{"a", "b"} {
		remote := filepath.Join(trial, "remote-"+name)
		if err = os.Mkdir(remote, 0700); err != nil {
			t.Fatal(err)
		}
		git(t, remote, "init", "-b", "main")
		if err = os.WriteFile(filepath.Join(remote, "source.txt"), []byte("fixture "+name), 0600); err != nil {
			t.Fatal(err)
		}
		git(t, remote, "add", ".")
		git(t, remote, "commit", "-m", "base")
		spec.Repositories = append(spec.Repositories, workspace.Repository{Name: name, Repository: remote})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	staged, err := store.StageWorkspaceContext(ctx, "managed-create1", spec)
	if err != nil {
		t.Fatal(err)
	}
	if staged == nil || staged.Workspace == nil || staged.Workspace.Phase != "initializing" || len(staged.Workspace.Members) != 2 || staged.Lease == nil || staged.Lease.Holder != "initialization" || staged.Lease.Token == "" || staged.Lease.Continuation == "" || staged.Lease.Execution != nil {
		t.Fatal("aggregate did not retain initialization ownership")
	}
	if staged.ProjectID == 0 || staged.QuotaBytes != total || staged.RequestedQuotaBytes != total || staged.QuotaInodes != policy.BenchInodes {
		t.Fatal("aggregate allocation differs from one requested total")
	}
	before, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	var assignments allocationLedger
	if err = json.Unmarshal(before, &assignments); err != nil {
		t.Fatal(err)
	}
	expected := len(prior.Assignments)
	for name, assignment := range prior.Assignments {
		if !reflect.DeepEqual(assignments.Assignments[name], assignment) {
			t.Fatal("staging changed a pre-existing allocation", name)
		}
	}
	for _, name := range []string{"store", "mirrors", "bench:card"} {
		if _, exists := prior.Assignments[name]; !exists {
			expected++
		}
	}
	if len(assignments.Assignments) != expected {
		t.Fatal("expected only one aggregate and any missing shared allocations")
	}
	for _, name := range []string{"store", "mirrors", "bench:card"} {
		if _, ok := assignments.Assignments[name]; !ok {
			t.Fatal("required allocation missing", name)
		}
	}
	a := assignments.Assignments["bench:card"]
	if a.Path != staged.Path || a.ProjectID != staged.ProjectID || a.Bytes != total || a.Inodes != staged.QuotaInodes {
		t.Fatal("bench ledger assignment does not match aggregate")
	}
	for _, path := range []string{staged.Path, staged.Workspace.Members[0].Path, staged.Workspace.Members[1].Path} {
		if got := projectID(t, path); got != staged.ProjectID {
			t.Fatal("member did not inherit the aggregate project", got)
		}
	}
	t.Log("PASS: two repository checkouts share one project, one total byte/inode allocation, and retained initialization ownership")
	retry, err := store.StageWorkspaceContext(ctx, "managed-create1", spec)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(retry, staged) {
		t.Fatal("retry changed aggregate identity, sources, allocation or ownership")
	}
	after, err := os.ReadFile(ledgerPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("retry changed quota ledger", err)
	}
	t.Log("PASS: exact retry preserves aggregate identity, initialization lease and the complete quota ledger")
	if old := os.Getenv("SUPERBENCH_WORKSPACE_OLD_CLI"); old != "" {
		checkCtx, done := context.WithTimeout(ctx, 15*time.Second)
		cmd := exec.CommandContext(checkCtx, old, "status", "--state", state, "--data", data)
		out, oldErr := cmd.CombinedOutput()
		done()
		if oldErr == nil || !strings.Contains(string(out), "unsupported state schema 18 (expected 13)") {
			t.Fatal("schema13 CLI did not refuse aggregate schema18", oldErr, string(out))
		}
		t.Log("PASS: schema13 CLI refuses schema18 before interpreting aggregate metadata")
	}
	for round := 0; round < 2; round++ {
		first := staged.Workspace.Members[round].Path
		second := staged.Workspace.Members[1-round].Path
		left := filepath.Join(first, "managed-quota-payload")
		right := filepath.Join(second, "managed-quota-payload")
		written, writeErr := writeSyncedPayload(left, 20<<20)
		if writeErr != nil || written != 20<<20 {
			t.Fatal("first member alone could not write below aggregate limit", written, writeErr)
		}
		other, quotaErr := writeSyncedPayload(right, 20<<20)
		if !errors.Is(quotaErr, syscall.EDQUOT) && !errors.Is(quotaErr, syscall.ENOSPC) {
			t.Fatal("combined member writes did not hit shared cap", written, other, quotaErr)
		}
		if other <= 0 || other >= 20<<20 || uint64(written+other) > total {
			t.Fatal("shared quota did not stop the combined allocation below its total", written, other)
		}
		for _, path := range []string{left, right} {
			if id := projectID(t, path); id != staged.ProjectID {
				t.Fatal("payload not charged to aggregate project", id)
			}
		}
		t.Logf("PASS: member %s wrote %d bytes; member %s hit shared quota after %d bytes; both individually below %d-byte total", staged.Workspace.Members[round].Name, written, staged.Workspace.Members[1-round].Name, other, total)
		for _, path := range []string{left, right} {
			if err = os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Log("PASS: aggregate staging only; initialization lease remains held and readiness/setup completion is not asserted")
}

func projectID(t *testing.T, path string) uint32 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "xfs_io", "-c", "stat", path).CombinedOutput()
	if err != nil {
		t.Fatal("read XFS project metadata", err, string(out))
	}
	match := regexp.MustCompile(`fsxattr\.projid\s*=\s*([0-9]+)`).FindSubmatch(out)
	if len(match) != 2 {
		t.Fatal("XFS project ID missing", string(out))
	}
	n, err := strconv.ParseUint(string(match[1]), 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	return uint32(n)
}

func writeSyncedPayload(path string, limit int64) (int64, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return 0, err
	}
	var written int64
	buf := bytes.Repeat([]byte{0x5a}, 1<<20)
	for written < limit {
		n, e := f.Write(buf[:min(int64(len(buf)), limit-written)])
		written += int64(n)
		if e == nil && n == 0 {
			e = fmt.Errorf("payload write made no progress")
		}
		if e == nil {
			e = f.Sync()
		}
		if e != nil {
			_ = f.Close()
			return written, e
		}
	}
	return written, f.Close()
}
