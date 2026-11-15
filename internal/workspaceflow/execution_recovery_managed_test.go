package workspaceflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceprep"
)

// This test runs only on the wrapper's disposable XFS and cgroup mounts.
func TestManagedWorkspaceRecoveryLifecycle(t *testing.T) {
	trial := os.Getenv("SUPERBENCH_WORKSPACE_MANAGED_TRIAL")
	if trial == "" {
		t.Skip("opt-in disposable Linux recovery drill")
	}
	helper, cgroups := os.Getenv("SUPERBENCH_WORKSPACE_QUOTA_HELPER"), os.Getenv("SUPERBENCH_WORKSPACE_CGROUP_ROOT")
	if runtime.GOOS != "linux" || os.Getuid() != 0 || os.Geteuid() != 0 || os.Getenv("SUDO_UID") != "" || !strings.HasPrefix(trial, "/var/tmp/superbench-workspace.") || filepath.Clean(trial) != trial || !filepath.IsAbs(helper) || !strings.HasPrefix(cgroups, "/sys/fs/cgroup/superbench-workspace-") {
		t.Fatal("recovery requires owned direct-root disposable fixture")
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
	} else if existing, e := os.ReadFile(policyPath); e != nil || !bytes.Equal(existing, raw) {
		t.Fatal("refusing different fixture policy", e)
	}
	store := bench.Store{State: state, Data: data, CgroupRoot: cgroups}
	if err := store.ConfigureHost(bench.HostConfig{DataRoot: data, QuotaHelper: helper, PolicyOverride: policyPath, MinDataFreeBytes: 8 << 20, MinStateFreeBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	spec := workspace.Spec{Key: "recovery-source", CheckpointRepository: "a", QuotaBytes: 32 << 20}
	for _, name := range []string{"a", "b"} {
		remote := filepath.Join(trial, "recovery-remote-"+name)
		if err := os.MkdirAll(filepath.Join(remote, ".bench"), 0700); err != nil {
			t.Fatal(err)
		}
		git(t, remote, "init", "-b", "main")
		config := fmt.Sprintf("[setup]\nonce = %q\neach = %q\n", "mkdir -p .bench/output; printf 'once\\n' >> .bench/output/once.log", "printf 'each\\n' >> .bench/output/each.log")
		for path, contents := range map[string]string{".bench/config.toml": config, ".gitignore": "/.bench/output/\n", "source.txt": "recovery source\n"} {
			if err := os.WriteFile(filepath.Join(remote, path), []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
		}
		git(t, remote, "add", ".")
		git(t, remote, "commit", "-m", "recovery fixture")
		spec.Repositories = append(spec.Repositories, workspace.Repository{Name: name, Repository: remote})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	source, err := store.InitializeWorkspaceContext(ctx, "managed-recovery-source", spec)
	if err != nil {
		t.Fatal(err)
	}
	held, err := store.AcquireWorkspaceContext(ctx, source.Key, "source-writer")
	if err != nil {
		t.Fatal(err)
	}
	started, release := filepath.Join(trial, "recovery-writer-started"), filepath.Join(trial, "recovery-writer-release")
	type outcome struct {
		result *workspaceprep.RunResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		r, e := store.RunWorkspaceContext(ctx, source.Key, held.Lease.Token, "a", []string{"/bin/sh", "-c", `printf ready > "$1"; while [ ! -e "$2" ]; do sleep .02; done`, "source", started, release}, false)
		done <- outcome{r, e}
	}()
	defer func() { _ = os.WriteFile(release, []byte("release"), 0600) }()
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		select {
		case result := <-done:
			t.Fatal("source writer failed before recovery", result.err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	beforeSource := snapshotWorkspace(t, store, source.Key)
	beforeFiles := tree(t, source.Path)
	if beforeSource.Lease == nil || beforeSource.Lease.Execution == nil || beforeSource.Lease.Execution.Phase != "running" {
		t.Fatal("source is not a live owned writer")
	}
	cp := source.Workspace.Checkpoint
	recovery := workspace.RecoverySpec{Key: "recovery-target", Repositories: append([]workspace.Repository(nil), spec.Repositories...), QuotaBytes: 32 << 20, Source: workspace.RecoverySource{Repository: spec.Repositories[0].Repository, BenchID: source.Workspace.ID, Commit: cp.Seal, ManifestSHA256: cp.Digest}}
	staged, err := store.StageWorkspaceRecoveryContext(ctx, "managed-recovery-target", recovery)
	if err != nil {
		t.Fatal(err)
	}
	if staged.Workspace.ID == source.Workspace.ID || staged.ProjectID == 0 || staged.ProjectID == source.ProjectID || staged.QuotaBytes != 32<<20 || staged.Lease == nil || staged.Lease.Token == held.Lease.Token || len(staged.Workspace.Preparation) != 0 {
		t.Fatal("recovery inherited source allocation, identity, owner or setup proof")
	}
	for _, member := range staged.Workspace.Members {
		if projectID(t, member.Path) != staged.ProjectID {
			t.Fatal("target members do not share their independent project")
		}
	}
	if previous := os.Getenv("SUPERBENCH_WORKSPACE_PREVIOUS_CLI"); previous != "" {
		before := snapshotWorkspace(t, store, staged.Key)
		checkCtx, done := context.WithTimeout(ctx, 15*time.Second)
		out, oldErr := exec.CommandContext(checkCtx, previous, "status", "--state", state, "--data", data).CombinedOutput()
		done()
		if oldErr == nil || !strings.Contains(string(out), "unsupported state schema 22 (expected 16)") {
			t.Fatal("schema16 CLI did not refuse recovery provenance", oldErr, string(out))
		}
		if !reflect.DeepEqual(before, snapshotWorkspace(t, store, staged.Key)) || !reflect.DeepEqual(beforeSource, snapshotWorkspace(t, store, source.Key)) {
			t.Fatal("schema16 status changed source or target state")
		}
		t.Log("PASS: schema16 CLI refuses schema22 without changing recovery or source ownership")
	}
	ready, err := store.RecoverWorkspaceContext(ctx, "managed-recovery-target", recovery)
	if err != nil {
		t.Fatal(err)
	}
	if ready.Lease != nil || ready.Workspace.Phase != "ready" || ready.Workspace.Checkpoint.Previous != "" {
		t.Fatal("target genesis did not release initialization owner")
	}
	for i, member := range ready.Workspace.Members {
		if member.Source != source.Workspace.Members[i].Source || git(t, member.Path, "rev-parse", "HEAD") != member.Source {
			t.Fatal("recovery did not use exact sealed source")
		}
		for _, name := range []string{"once", "each"} {
			raw, err := os.ReadFile(filepath.Join(member.Path, ".bench", "output", name+".log"))
			if err != nil || string(raw) != name+"\n" {
				t.Fatal("new identity inherited/skipped source hook receipts", member.Name, name, err, string(raw))
			}
		}
	}
	runs, err := store.History(ready.Key, 32)
	if err != nil || len(runs) != 2 {
		t.Fatal("new target hook history missing", len(runs), err)
	}
	for _, run := range runs {
		if run.File == nil || run.ExitCode == nil || *run.ExitCode != 0 || run.ReleasedAt.IsZero() || run.Checkpoint != ready.Workspace.Checkpoint.Seal {
			t.Fatal("target setup history not sealed/released against new genesis")
		}
		if _, err := os.Stat(filepath.Join(cgroups, run.ID)); !os.IsNotExist(err) {
			t.Fatal("target setup scope survived handoff", err)
		}
	}
	if !reflect.DeepEqual(beforeSource, snapshotWorkspace(t, store, source.Key)) || !reflect.DeepEqual(beforeFiles, tree(t, source.Path)) {
		t.Fatal("recovery changed running source ownership or checkout")
	}
	select {
	case result := <-done:
		t.Fatal("recovery stopped the source writer", result.err)
	default:
	}
	beforeReplay := tree(t, ready.Path)
	replay, err := store.RecoverWorkspaceContext(ctx, "managed-recovery-target", recovery)
	if err != nil || !reflect.DeepEqual(ready, replay) || !reflect.DeepEqual(beforeReplay, tree(t, ready.Path)) {
		t.Fatal("warm recovery replay reran target hooks", err)
	}
	if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.err != nil || result.result == nil || result.result.ExitCode != 0 || result.result.Bench.Lease.Token != held.Lease.Token {
			t.Fatal("source writer did not complete with original owner", result.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	released, err := store.ReleaseWorkspaceContext(ctx, source.Key, held.Lease.Token)
	if err != nil || released.Lease != nil {
		t.Fatal("source owner could not release unchanged sealed work", err)
	}
	t.Log("PASS: exact-seal recovery creates fresh hook receipts/genesis and independent aggregate project while preserving a running source owner; both supervisors fence and source releases normally")
}
