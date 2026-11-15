package workspaceflow

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

func TestManagedWorkspaceArchiveLifecycle(t *testing.T) {
	trial := os.Getenv("SUPERBENCH_WORKSPACE_MANAGED_TRIAL")
	if trial == "" {
		t.Skip("opt-in disposable XFS/cgroup archive drill")
	}
	helper, cgroups := os.Getenv("SUPERBENCH_WORKSPACE_QUOTA_HELPER"), os.Getenv("SUPERBENCH_WORKSPACE_CGROUP_ROOT")
	if runtime.GOOS != "linux" || os.Getuid() != 0 || os.Geteuid() != 0 || os.Getenv("SUDO_UID") != "" || !strings.HasPrefix(trial, "/var/tmp/superbench-workspace.") || filepath.Clean(trial) != trial || !filepath.IsAbs(helper) || !strings.HasPrefix(cgroups, "/sys/fs/cgroup/superbench-workspace-") {
		t.Fatal("archive requires owned direct-root disposable fixture")
	}
	data, state := filepath.Join(trial, "data"), filepath.Join(trial, "state")
	policy := hoststorage.Policy{Root: data, Ledger: filepath.Join(trial, "quota-ledger"), OwnerUID: 0, OwnerGID: 0, BenchBytes: 64 << 20, StoreBytes: 32 << 20, MirrorBytes: 64 << 20, ReserveBytes: 64 << 20, BenchInodes: 4096, StoreInodes: 8192, MirrorInodes: 8192, ReserveInodes: 1024}
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(trial, "policy.json")
	f, err := os.OpenFile(policyPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		_, err = f.Write(raw)
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			t.Fatal(err, closeErr)
		}
	} else if !os.IsExist(err) {
		t.Fatal(err)
	} else if existing, e := os.ReadFile(policyPath); e != nil || !bytes.Equal(raw, existing) {
		t.Fatal("refusing different managed policy", e)
	}
	store := bench.Store{State: state, Data: data, CgroupRoot: cgroups}
	if err := store.ConfigureHost(bench.HostConfig{DataRoot: data, QuotaHelper: helper, PolicyOverride: policyPath, MinDataFreeBytes: 8 << 20, MinStateFreeBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	spec := workspace.Spec{Key: "archive-managed", CheckpointRepository: "a", QuotaBytes: 32 << 20}
	for _, name := range []string{"a", "b"} {
		remote := filepath.Join(trial, "archive-managed-remote-"+name)
		if err := os.Mkdir(remote, 0700); err != nil {
			t.Fatal(err)
		}
		git(t, remote, "init", "-b", "main")
		if err := os.WriteFile(filepath.Join(remote, "source.txt"), []byte("managed archive source\n"), 0600); err != nil {
			t.Fatal(err)
		}
		git(t, remote, "add", ".")
		git(t, remote, "commit", "-m", "archive fixture")
		spec.Repositories = append(spec.Repositories, workspace.Repository{Name: name, Repository: remote})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ready, err := store.InitializeWorkspaceContext(ctx, "archive-managed-create", spec)
	if err != nil {
		t.Fatal(err)
	}
	if ready.ProjectID == 0 || ready.QuotaBytes != 32<<20 {
		t.Fatal("managed aggregate allocation missing")
	}
	for _, member := range ready.Workspace.Members {
		if projectID(t, member.Path) != ready.ProjectID {
			t.Fatal("members do not share aggregate quota")
		}
	}
	archived, err := store.ArchiveWorkspaceContext(ctx, ready.Key, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertReadOnly(t, ready.Path)
	// Remove DAC bypass for this probe while retaining the actual file owner.
	// A failed write is meaningful only if that same process can first read it.
	if setpriv, err := exec.LookPath("setpriv"); err == nil {
		for _, member := range ready.Workspace.Members {
			cmd := exec.CommandContext(ctx, setpriv, "--bounding-set=-dac_override,-dac_read_search", "--", "/bin/sh", "-c", `test -r "$1" && ! printf forbidden >> "$1"`, "probe", filepath.Join(member.Path, "source.txt"))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatal("frozen owner could write or could not read", err, string(out))
			}
		}
		t.Log("PASS: actual file owner can read but cannot write archived member bytes after dropping DAC bypass")
	} else {
		t.Log("setpriv unavailable; archive mode assertion remains checked")
	}
	if previous := os.Getenv("SUPERBENCH_WORKSPACE_ARCHIVE_PREVIOUS_CLI"); previous != "" {
		before := snapshotWorkspace(t, store, ready.Key)
		checkCtx, done := context.WithTimeout(ctx, 15*time.Second)
		out, oldErr := exec.CommandContext(checkCtx, previous, "status", "--state", state, "--data", data).CombinedOutput()
		done()
		if oldErr == nil || !strings.Contains(string(out), "unsupported state schema 20 (expected 17)") {
			t.Fatal("schema17 CLI did not refuse aggregate archive state", oldErr, string(out))
		}
		if !reflect.DeepEqual(before, snapshotWorkspace(t, store, ready.Key)) {
			t.Fatal("old CLI changed archived record")
		}
		t.Log("PASS: schema17 CLI refuses schema20 without changing archive authority")
	}
	plan, err := store.PlanWorkspaceReapContext(ctx, ready.Key)
	if err != nil {
		t.Fatal(err)
	}
	reaped, err := store.ReapWorkspaceContext(ctx, *plan)
	if err != nil {
		t.Fatal(err)
	}
	if reaped.Workspace.Phase != "reaped" || reaped.Workspace.Archive.ReapedAt == "" {
		t.Fatal("managed reap did not commit terminal metadata")
	}
	if _, err := os.Lstat(ready.Path); !os.IsNotExist(err) {
		t.Fatal("quota release did not remove empty aggregate root", err)
	}
	var ledger struct {
		Assignments map[string]hoststorage.Assignment
	}
	raw, err = os.ReadFile(filepath.Join(policy.Ledger, "assignments.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &ledger); err != nil {
		t.Fatal(err)
	}
	retired, ok := ledger.Assignments["bench:"+ready.Key]
	if !ok || retired.ProjectID != ready.ProjectID || retired.Bytes != 0 || retired.Inodes != 0 {
		t.Fatal("reap did not retain retired quota identity", retired)
	}
	replay, err := store.ReapWorkspaceContext(ctx, *plan)
	if err != nil || !reflect.DeepEqual(reaped, replay) {
		t.Fatal("managed reap retry changed completion", err)
	}
	replay, err = store.InitializeWorkspaceContext(ctx, "archive-managed-create", spec)
	if err != nil || !reflect.DeepEqual(reaped, replay) {
		t.Fatal("creation replay recreated retired managed root", err)
	}
	if _, err := os.Lstat(archived.Path); !os.IsNotExist(err) {
		t.Fatal("replay recreated retired root", err)
	}
	t.Log("PASS: two-member read-only archive reaps its approved tree, retires one quota project, and preserves replay without recreating storage")
}
