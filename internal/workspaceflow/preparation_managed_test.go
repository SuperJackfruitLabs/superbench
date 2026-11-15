package workspaceflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/checkpointset"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
)

// TestManagedWorkspacePreparationLifecycle runs only inside the disposable
// mount/cgroup fixture. Every hook, marker and allocation belongs to that trial.
func TestManagedWorkspacePreparationLifecycle(t *testing.T) {
	trial := os.Getenv("SUPERBENCH_WORKSPACE_MANAGED_TRIAL")
	if trial == "" {
		t.Skip("opt-in disposable Linux drill; use scripts/test-workspace-staging.sh")
	}
	if runtime.GOOS != "linux" || os.Getuid() != 0 || os.Geteuid() != 0 || os.Getenv("SUDO_UID") != "" || !strings.HasPrefix(trial, "/var/tmp/superbench-workspace.") || filepath.Clean(trial) != trial {
		t.Fatal("managed preparation requires the direct-root disposable Linux fixture")
	}
	helper, cgroups := os.Getenv("SUPERBENCH_WORKSPACE_QUOTA_HELPER"), os.Getenv("SUPERBENCH_WORKSPACE_CGROUP_ROOT")
	if !filepath.IsAbs(helper) || !strings.HasPrefix(cgroups, "/sys/fs/cgroup/superbench-workspace-") {
		t.Fatal("owned fixture paths missing")
	}
	data, state := filepath.Join(trial, "data"), filepath.Join(trial, "state")
	policyPath := filepath.Join(trial, "policy.json")
	// Match the aggregate-allocation drill's policy exactly. Refuse a changed
	// policy rather than reconfiguring an existing fixture's privileged ledger.
	policy := hoststorage.Policy{Root: data, Ledger: filepath.Join(trial, "quota-ledger"), OwnerUID: 0, OwnerGID: 0, BenchBytes: 64 << 20, StoreBytes: 32 << 20, MirrorBytes: 64 << 20, ReserveBytes: 64 << 20, BenchInodes: 4096, StoreInodes: 8192, MirrorInodes: 8192, ReserveInodes: 1024}
	raw, err := os.ReadFile(policyPath)
	if os.IsNotExist(err) {
		raw, err = json.Marshal(policy)
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(policyPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.Write(raw)
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			t.Fatal(err, closeErr)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	var actual hoststorage.Policy
	if err := json.Unmarshal(raw, &actual); err != nil || !reflect.DeepEqual(actual, policy) {
		t.Fatal("existing trial policy differs; refusing replacement", err)
	}
	store := bench.Store{State: state, Data: data, CgroupRoot: cgroups}
	if err := store.ConfigureHost(bench.HostConfig{DataRoot: data, QuotaHelper: helper, PolicyOverride: policyPath, MinDataFreeBytes: 8 << 20, MinStateFreeBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("/usr/bin/setsid"); err != nil {
		t.Fatal("setsid required for descendant-fencing proof", err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	fail := filepath.Join(trial, "warm-fail-second")
	childReady, releaseChild := filepath.Join(trial, "warm-child-ready"), filepath.Join(trial, "warm-release-child")
	childGroup, late := filepath.Join(trial, "warm-child-cgroup"), filepath.Join(trial, "warm-late-write")
	if err := os.WriteFile(fail, []byte("fail once"), 0600); err != nil {
		t.Fatal(err)
	}
	child := "cat /proc/self/cgroup > \"$1\"; printf ready > \"$2\"; while [ ! -e \"$3\" ]; do sleep .01; done; printf escaped > \"$4\""
	spec := workspace.Spec{Key: "warm", CheckpointRepository: "a", QuotaBytes: 32 << 20}
	for _, name := range []string{"a", "b"} {
		remote := filepath.Join(trial, "warm-remote-"+name)
		if err := os.Mkdir(remote, 0700); err != nil {
			t.Fatal(err)
		}
		git(t, remote, "init", "-b", "main")
		if err := os.Mkdir(filepath.Join(remote, ".bench"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(remote, ".gitignore"), []byte("/.bench/output/\n"), 0600); err != nil {
			t.Fatal(err)
		}
		once := "mkdir -p .bench/output\nprintf 'once\\n' >> .bench/output/once.log\n"
		each := "printf 'each\\n' >> .bench/output/each.log\n"
		if name == "a" {
			once += "/usr/bin/setsid /bin/sh -c " + quote(child) + " child " + quote(childGroup) + " " + quote(childReady) + " " + quote(releaseChild) + " " + quote(late) + " </dev/null >/dev/null 2>&1 &\n" +
				"i=0; while [ ! -e " + quote(childReady) + " ]; do i=$((i+1)); [ \"$i\" -lt 200 ] || exit 31; sleep .01; done\n"
		} else {
			each += "printf release > " + quote(releaseChild) + "\nsleep .15\n[ ! -e " + quote(late) + " ] || exit 32\nif [ -e " + quote(fail) + " ]; then printf 'deliberate second-member failure\\n'; exit 17; fi\n"
		}
		config := fmt.Sprintf("[quota]\nsize = \"32MiB\"\n[setup]\nonce = %q\neach = %q\n", once, each)
		if err := os.WriteFile(filepath.Join(remote, ".bench", "config.toml"), []byte(config), 0600); err != nil {
			t.Fatal(err)
		}
		git(t, remote, "add", ".")
		git(t, remote, "commit", "-m", "managed preparation fixture")
		spec.Repositories = append(spec.Repositories, workspace.Repository{Name: name, Repository: remote})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	held, err := store.InitializeWorkspaceContext(ctx, "managed-warm1", spec)
	if err == nil || held == nil || held.Workspace == nil || held.Lease == nil || held.Workspace.Phase != "initializing" || held.Lease.Holder != "initialization" || held.Lease.Continuation == "" || held.Lease.StopRequested {
		t.Fatal("second-member failure did not preserve initialization ownership", err)
	}
	if len(held.Workspace.Preparation) != 1 || held.Workspace.Preparation[0].Name != "a" || held.Workspace.Preparation[0].RunID == "" || held.Lease.Execution == nil || held.Lease.Execution.Phase != "stopped" || held.Workspace.Checkpoint != nil {
		t.Fatal("partial preparation lost completed first-member proof or stopped failed execution")
	}
	firstProof := held.Workspace.Preparation[0]
	group, err := os.ReadFile(childGroup)
	if err != nil || !strings.Contains(string(group), "/"+filepath.Base(cgroups)+"/"+firstProof.RunID) {
		t.Fatal("setsid descendant was not in the first member's supervised scope", err, string(group))
	}
	if _, err := os.Lstat(late); !os.IsNotExist(err) {
		t.Fatal("first-member descendant wrote after handoff", err)
	}
	if _, err := os.Stat(filepath.Join(cgroups, firstProof.RunID)); !os.IsNotExist(err) {
		t.Fatal("first-member scope was not fenced before second-member execution", err)
	}
	for _, member := range held.Workspace.Members {
		if out := git(t, member.Mirror, "ls-remote", "--refs", "origin", "refs/heads/"+member.Branch); out != "" {
			t.Fatal("partial preparation published a task branch before all-member success")
		}
	}
	before, err := store.History(spec.Key, 10)
	if err != nil || len(before) != 2 {
		t.Fatal("partial lifecycle history missing", err, len(before))
	}
	for _, r := range before {
		if r.File == nil || r.ExitCode == nil || r.SealedAt.IsZero() || !r.ReleasedAt.IsZero() {
			t.Fatal("partial stage evidence was unsealed or prematurely released")
		}
	}
	t.Log("PASS: second-member failure retains the same initialization owner, sealed stage evidence and first-member proof; its detached descendant is fenced before handoff")
	if err := os.Remove(fail); err != nil {
		t.Fatal(err)
	}
	resumed, err := store.StageWorkspaceContext(ctx, "managed-warm1", spec)
	if err != nil || resumed == nil || resumed.Lease == nil || resumed.Lease.Token != held.Lease.Token || resumed.Lease.Continuation != held.Lease.Continuation || !reflect.DeepEqual(resumed.Workspace.Preparation, held.Workspace.Preparation) {
		t.Fatal("retry replaced the held initialization owner or first-member proof", err)
	}
	ready, err := store.InitializeWorkspaceContext(ctx, "managed-warm1", spec)
	if err != nil {
		t.Fatal(err)
	}
	if ready == nil || ready.Workspace == nil || ready.Workspace.Phase != "ready" || ready.Lease != nil || ready.Workspace.Pending != nil || ready.Workspace.Checkpoint == nil || ready.Workspace.ID != held.Workspace.ID || ready.ProjectID != held.ProjectID || ready.QuotaBytes != 32<<20 || ready.QuotaInodes != policy.BenchInodes {
		t.Fatal("successful retry did not publish one completely initialized, sealed aggregate")
	}
	if len(ready.Workspace.Preparation) != 2 || ready.Workspace.Preparation[0] != firstProof {
		t.Fatal("retry repeated or replaced first-member preparation proof")
	}
	for _, member := range ready.Workspace.Members {
		want := 1
		if member.Name == "b" {
			want = 2
		}
		for _, kind := range []string{"once", "each"} {
			raw, err := os.ReadFile(filepath.Join(member.Path, ".bench", "output", kind+".log"))
			if err != nil || !bytes.Equal(raw, bytes.Repeat([]byte(kind+"\n"), want)) {
				t.Fatal("unexpected hook repetition", member.Name, kind, string(raw), err)
			}
		}
		if projectID(t, member.Path) != ready.ProjectID || projectID(t, filepath.Join(member.Path, ".bench", "output", "each.log")) != ready.ProjectID {
			t.Fatal("preparation output escaped the one aggregate allocation")
		}
	}
	cp := ready.Workspace.Checkpoint
	manifest, err := workspacepolicy.Manifest(ready, cp)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(manifest)
	if err != nil || checkpointset.Digest(canonical) != cp.Digest {
		t.Fatal("persisted generation digest differs", err)
	}
	for i, member := range ready.Workspace.Members {
		for _, ref := range []string{"refs/heads/" + member.Branch, manifest.Members[i].PinRef} {
			fields := strings.Fields(git(t, member.Mirror, "ls-remote", "--refs", "origin", ref))
			if len(fields) != 2 || fields[0] != cp.Members[i].Commit || fields[1] != ref {
				t.Fatal("remote task/pin vector differs from complete generation")
			}
		}
		if member.Name == ready.Workspace.CheckpointRepository {
			meta, _ := checkpointset.MetadataRef(ready.Workspace.ID)
			fields := strings.Fields(git(t, member.Mirror, "ls-remote", "--refs", "origin", meta))
			if len(fields) != 2 || fields[0] != cp.Seal || fields[1] != meta {
				t.Fatal("remote metadata seal missing")
			}
			if raw := git(t, member.Mirror, "show", cp.Seal+":checkpoint.json"); raw != string(canonical) {
				t.Fatal("remote seal contents differ from canonical complete vector")
			}
		}
	}
	after, err := store.History(spec.Key, 10)
	if err != nil || len(after) != 3 {
		t.Fatal("retry did not preserve exactly three stage histories", err, len(after))
	}
	successes, failures := 0, 0
	for _, r := range after {
		if r.File == nil || r.ExitCode == nil || r.SealedAt.IsZero() || r.ReleasedAt.IsZero() || r.Checkpoint != cp.Seal {
			t.Fatal("stage history was not sealed and released to the aggregate checkpoint")
		}
		if *r.ExitCode == 0 {
			successes++
		} else if *r.ExitCode == 17 {
			failures++
		} else {
			t.Fatal("unexpected setup exit", *r.ExitCode)
		}
		raw, err := os.ReadFile(filepath.Join(state, r.File.Path))
		sum := sha256.Sum256(raw)
		if err != nil || int64(len(raw)) != r.Size || hex.EncodeToString(sum[:]) != r.File.SHA256 {
			t.Fatal("sealed history bytes changed", err)
		}
		if _, err := os.Stat(filepath.Join(cgroups, r.ID)); !os.IsNotExist(err) {
			t.Fatal("released preparation left a live execution scope", err)
		}
	}
	if successes != 2 || failures != 1 {
		t.Fatal("preparation history outcome counts differ")
	}
	if _, err := os.Lstat(late); !os.IsNotExist(err) {
		t.Fatal("descendant survived completed initialization", err)
	}
	t.Log("PASS: retry skips the completed first member, reruns the failed second member, seals the whole remote vector and releases all three stage histories before readiness")
	// A replay returns the same ready identity and does not recreate ownership or
	// repeat either repository's hooks after that ownership has ended.
	replay, err := store.InitializeWorkspaceContext(ctx, "managed-warm1", spec)
	if err != nil || !reflect.DeepEqual(replay, ready) {
		t.Fatal("completed initialization replay changed identity or ownership", err)
	}
	last, err := store.History(spec.Key, 10)
	if err != nil || !reflect.DeepEqual(last, after) {
		t.Fatal("completed replay repeated preparation", err)
	}
	assertManagedWorkspaceLedger(t, policy.Ledger, ready)
	t.Log("PASS: complete initialization replay preserves readiness and one total quota allocation without recreating a lease")
}

func assertManagedWorkspaceLedger(t *testing.T, ledger string, ready *model.Bench) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(ledger, "assignments.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Assignments map[string]hoststorage.Assignment
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	a, ok := result.Assignments["bench:"+ready.Key]
	if !ok || a.Path != ready.Path || a.ProjectID != ready.ProjectID || a.Bytes != ready.QuotaBytes || a.Inodes != ready.QuotaInodes {
		t.Fatal("warm lifecycle has no matching aggregate allocation")
	}
	for key, assignment := range result.Assignments {
		if strings.HasPrefix(key, "bench:"+ready.Key+"/") || assignment.ProjectID == ready.ProjectID && key != "bench:"+ready.Key {
			t.Fatal("member acquired a separate quota or shared another allocation")
		}
	}
}
