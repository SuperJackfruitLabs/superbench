package workspaceflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

func TestManagedWorkspaceExecutionLifecycle(t *testing.T) {
	trial := os.Getenv("SUPERBENCH_WORKSPACE_MANAGED_TRIAL")
	if trial == "" {
		t.Skip("opt-in disposable XFS/cgroup drill; use scripts/test-workspace-staging.sh")
	}
	cgroups, helper := os.Getenv("SUPERBENCH_WORKSPACE_CGROUP_ROOT"), os.Getenv("SUPERBENCH_WORKSPACE_QUOTA_HELPER")
	if runtime.GOOS != "linux" || os.Getuid() != 0 || os.Geteuid() != 0 || os.Getenv("SUDO_UID") != "" || !strings.HasPrefix(trial, "/var/tmp/superbench-workspace.") || filepath.Clean(trial) != trial || !strings.HasPrefix(cgroups, "/sys/fs/cgroup/superbench-workspace-") || !filepath.IsAbs(helper) {
		t.Fatal("execution drill requires the owned direct-root Linux fixture")
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
		if closeErr := f.Close(); err != nil || closeErr != nil {
			t.Fatal(err, closeErr)
		}
	} else if !os.IsExist(err) {
		t.Fatal(err)
	} else if existing, e := os.ReadFile(policyPath); e != nil || !bytes.Equal(raw, existing) {
		t.Fatal("refusing to replace a different managed policy", e)
	}
	store := bench.Store{State: state, Data: data, CgroupRoot: cgroups}
	if err := store.ConfigureHost(bench.HostConfig{DataRoot: data, QuotaHelper: helper, PolicyOverride: policyPath, MinDataFreeBytes: 8 << 20, MinStateFreeBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	spec := workspace.Spec{Key: "operational", CheckpointRepository: "a", QuotaBytes: 32 << 20}
	for _, name := range []string{"a", "b"} {
		remote := filepath.Join(trial, "operational-remote-"+name)
		if err := os.MkdirAll(filepath.Join(remote, ".bench"), 0700); err != nil {
			t.Fatal(err)
		}
		git(t, remote, "init", "-b", "main")
		config := fmt.Sprintf("[setup]\nonce = %q\neach = %q\n", "mkdir -p .bench/output; printf 'once\\n' >> .bench/output/once.log", "printf 'each\\n' >> .bench/output/each.log")
		for path, contents := range map[string]string{".gitignore": "/.bench/output/\n", ".bench/config.toml": config, "source.txt": "base\n"} {
			if err := os.WriteFile(filepath.Join(remote, path), []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
		}
		git(t, remote, "add", ".")
		git(t, remote, "commit", "-m", "execution fixture")
		spec.Repositories = append(spec.Repositories, workspace.Repository{Name: name, Repository: remote})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ready, err := store.InitializeWorkspaceContext(ctx, "operational-create1", spec)
	if err != nil {
		t.Fatal(err)
	}
	held, err := store.AcquireWorkspaceContext(ctx, spec.Key, "first-stage")
	if err != nil {
		t.Fatal(err)
	}
	token := held.Lease.Token
	childReady, releaseChild, late := filepath.Join(trial, "payload-child-ready"), filepath.Join(trial, "payload-child-release"), filepath.Join(trial, "payload-late-write")
	child := `printf ready > "$1"; while [ ! -e "$2" ]; do sleep .01; done; printf escaped > "$3"`
	command := `test -f source.txt; printf 'task change\n' >> source.txt
printf '\n# payload change\n' >> .bench/config.toml
git -c user.name=fixture -c user.email=fixture@example.invalid add source.txt .bench/config.toml
git -c user.name=fixture -c user.email=fixture@example.invalid commit -m 'payload checkpoint'
/usr/bin/setsid /bin/sh -c "$1" child "$2" "$3" "$4" </dev/null >/dev/null 2>&1 &
i=0; while [ ! -e "$2" ]; do i=$((i+1)); [ "$i" -lt 200 ] || exit 31; sleep .01; done`
	result, err := store.RunWorkspaceContext(ctx, spec.Key, token, "a", []string{"/bin/sh", "-c", command, "payload", child, childReady, releaseChild, late}, false, nil)
	if err != nil || result == nil || result.ExitCode != 0 || result.Bench.Lease == nil || result.Bench.Lease.Token != token || result.Execution.Phase != "stopped" {
		t.Fatal("payload did not preserve its stopped owner", err)
	}
	if _, err := os.Stat(filepath.Join(cgroups, result.Execution.ID)); !os.IsNotExist(err) {
		t.Fatal("payload descendants were not fenced", err)
	}
	if err := os.WriteFile(releaseChild, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReleaseWorkspaceContext(ctx, spec.Key, token); err == nil {
		t.Fatal("changed payload released against old genesis")
	}
	cp, err := store.CheckpointWorkspaceContext(ctx, spec.Key, token)
	if err != nil || cp.Workspace.Checkpoint.Seal == ready.Workspace.Checkpoint.Seal {
		t.Fatal("payload whole-vector checkpoint missing", err)
	}
	released, err := store.ReleaseWorkspaceContext(ctx, spec.Key, token)
	if err != nil || released.Lease != nil {
		t.Fatal("payload configuration change prevented safe release", err)
	}
	next, err := store.AcquireWorkspaceContext(ctx, spec.Key, "second-stage")
	if err != nil || next.Lease.Token == token {
		t.Fatal("second-stage handoff failed", err)
	}
	for _, member := range next.Workspace.Members {
		once := 1
		if member.Name == "a" {
			once = 2 // The payload changed this member's configuration bytes.
		}
		for kind, count := range map[string]int{"once": once, "each": 3} {
			raw, err := os.ReadFile(filepath.Join(member.Path, ".bench", "output", kind+".log"))
			if err != nil || !bytes.Equal(raw, bytes.Repeat([]byte(kind+"\n"), count)) {
				t.Fatal("acquisition once/each semantics changed", member.Name, kind, string(raw), err)
			}
		}
	}
	failed, err := store.RunWorkspaceContext(ctx, spec.Key, next.Lease.Token, "", []string{"/bin/sh", "-c", "test -d a && test -d b && exit 23"}, false, nil)
	if err == nil || failed == nil || failed.ExitCode != 23 || failed.Bench.Lease == nil || failed.Execution.Phase != "stopped" {
		t.Fatal("root payload failure lost the stopped owner", err)
	}
	if _, err := store.StopWorkspace(spec.Key, next.Lease.Token, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RunWorkspaceContext(ctx, spec.Key, next.Lease.Token, "b", []string{"/bin/true"}, false, nil); err == nil {
		t.Fatal("explicit stop revived an automatic payload continuation")
	}
	if _, err := store.CheckpointWorkspaceContext(ctx, spec.Key, next.Lease.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReleaseWorkspaceContext(ctx, spec.Key, next.Lease.Token); err != nil {
		t.Fatal("sealed failed payload could not release after explicit stop", err)
	}
	history, err := store.History(spec.Key, 20)
	if err != nil || len(history) != 8 {
		t.Fatal("stage history count differs", len(history), err)
	}
	for _, run := range history {
		if run.File == nil || run.ExitCode == nil || run.ReleasedAt.IsZero() || run.Checkpoint == "" {
			t.Fatal("handoff lost protected sealed history")
		}
	}
	if _, err := os.Stat(late); !os.IsNotExist(err) {
		t.Fatal("detached payload wrote after handoff", err)
	}
	assertManagedWorkspaceLedger(t, policy.Ledger, released)
	t.Log("PASS: two operational acquisitions preserve one allocation, once/each setup, member/root cwd, descendant fencing, changed-config checkpoints, failed-payload evidence, explicit-stop revocation and atomic history release")
}
