//go:build linux && freshstoragefixture

package payloadbootstrap_test

// This fixture deliberately does NOT call InitializeFresh or claim the booted
// systemd install gate. A root supervisor supplies a new private XFS mount,
// original PID namespace, private mount/net namespaces and an owned cgroup.
// No account, host policy, service, sudo rule or network is configured here.
// Calls target the real manager Bootstrap API, not the command/daemon route.
// On failure all filesystem evidence is retained. The supervisor must preserve
// the fixture if any helper/unknown writer remains; never kill a quota helper
// mid-transaction merely to make cleanup pass.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperationjournal"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/serviceidentity"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storagecustody"
)

func TestFreshIndexedMaterializerLowerFixture(t *testing.T) {
	if os.Getuid() != 0 || os.Geteuid() != 0 || os.Getenv("SUPERBENCH_FRESH_LOWER_FIXTURE") != "1" || os.Getenv("SUDO_UID") != "" {
		t.Fatal("explicit direct-root lower fixture required")
	}
	for _, kind := range []string{"mnt", "net"} {
		original, err := os.Readlink("/proc/1/ns/" + kind)
		if err != nil || os.Getenv("SUPERBENCH_LOWER_ORIGINAL_"+strings.ToUpper(kind)) != original {
			t.Fatal("original namespace proof missing", kind, err)
		}
		a, err := os.Stat("/proc/1/ns/" + kind)
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.Stat("/proc/self/ns/" + kind)
		if err != nil || os.SameFile(a, b) {
			t.Fatal("private namespace required", kind, err)
		}
	}
	a, err := os.Stat("/proc/1/ns/pid")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat("/proc/self/ns/pid")
	if err != nil || !os.SameFile(a, b) {
		t.Fatal("original PID namespace required", err)
	}
	if err = requireUnusedLowerIdentities(); err != nil {
		t.Fatal(err)
	}
	t.Log("LOWER_UNUSED_UID_GID_PROCESS_INVENTORY_PASS")
	base := os.Getenv("SUPERBENCH_LOWER_TRIAL")
	if filepath.Dir(base) != "/var/tmp" || !strings.HasPrefix(filepath.Base(base), "superbench-fresh-lower-") {
		t.Fatal("owned lower fixture path required")
	}
	if err = storagecustody.RootOwnedDirectory(base, false); err != nil {
		t.Fatal(err)
	}
	if s := mustLowerInfo(t, base); s.Mode().Perm() != 0755 {
		t.Fatal("owned fixture ancestor must permit exact daemon traversal")
	}
	marker, err := storagecustody.ReadProtected(filepath.Join(base, "fixture-owner"), 128)
	if err != nil || !strings.HasPrefix(string(marker), "superbench-fresh-lower.v1\n") {
		t.Fatal("owned lower fixture marker required", err)
	}
	helper := os.Getenv("SUPERBENCH_LOWER_HELPER")
	expectedSHA := os.Getenv("SUPERBENCH_LOWER_HELPER_SHA256")
	verifyLowerHelper(t, helper, expectedSHA)
	cgroup := os.Getenv("SUPERBENCH_LOWER_CGROUP")
	if filepath.Dir(cgroup) != "/sys/fs/cgroup" || !strings.HasPrefix(filepath.Base(cgroup), "superbench-fresh-lower-") {
		t.Fatal("owned lower cgroup required")
	}
	var cgfs syscall.Statfs_t
	if syscall.Statfs(cgroup, &cgfs) != nil || cgfs.Type != 0x63677270 {
		t.Fatal("actual cgroup v2 required")
	}
	p := hoststorage.Policy{OperationJournal: "indexed-v2", Root: filepath.Join(base, "data"), Ledger: filepath.Join(base, "privileged", "ledger"), OwnerUID: 61001, OwnerGID: 61003, BenchBytes: 32 << 20, StoreBytes: 16 << 20, MirrorBytes: 32 << 20, ReserveBytes: 32 << 20, BenchInodes: 4096, StoreInodes: 4096, MirrorInodes: 4096, ReserveInodes: 4096}
	private := payloadexec.Policy{DaemonUID: 61001, DaemonGID: 61003, PayloadUID: 61002, PayloadGID: 61004, DataRoot: p.Root, StateRoot: filepath.Join(base, "state"), CgroupRoot: cgroup}
	if err = p.Validate(); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Dir(p.Ledger), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(private.StateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Chown(private.StateRoot, int(private.DaemonUID), int(private.DaemonGID)); err != nil {
		t.Fatal(err)
	}

	// Private /etc is only for the real installed-policy Open/helper route. It
	// cannot simulate systemd's booted-host launch-exclusion proof.
	etc := filepath.Join(base, "etc")
	if err = os.Mkdir(etc, 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(etc, "superbench"), 0755); err != nil {
		t.Fatal(err)
	}
	machine, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(etc, "machine-id"), machine, 0444); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(etc, "superbench", "storage.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	raw, err = private.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(etc, "superbench", "payload-policy.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	registry, err := serviceidentity.ForPolicy(private)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = registry.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(etc, "superbench", "service-identity.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	for path, mode := range map[string]os.FileMode{etc: 0755, filepath.Join(etc, "superbench"): 0755, filepath.Join(etc, "machine-id"): 0444, filepath.Join(etc, "superbench", "service-identity.json"): 0644} {
		if err = os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	if err = syscall.Mount(etc, "/etc", "", syscall.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	// Fixed-policy Control retains installation FDs for its process lifetime.
	// The outer supervisor must wait for this process and namespace to end;
	// an in-process unmount would incorrectly fail EBUSY with valid authority.

	old, closeObservation, err := initializeLowerRecords(t, p)
	if err != nil {
		t.Fatal(err)
	}
	closeObservation()
	if err = old.Run(t.Context(), func() error { t.Error("expired lower observation reused"); return nil }); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("expired physical observation granted effects: %v", err)
	}
	control, actual, err := quotaoperationcontrol.Open(t.Context())
	if err != nil || actual.Root != p.Root {
		t.Fatalf("actual fixed existing Open: %+v %v", actual, err)
	}

	t.Log("LOWER_REAL_HEADER_ANCHOR_AND_FIXED_OPEN_PASS")
	authority, err := p.OperationAuthority(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	identityFor := func(label string) quotaoperation.Identity {
		return quotaoperation.Identity{OperationID: lowerHash("operation:" + label + string(marker)), Epoch: lowerHash("epoch:" + label + string(marker))[:32], StateIdentity: lowerHash("synthetic-lower-state:" + label + string(marker)), Authority: authority, BinarySHA256: expectedSHA}
	}
	identity := identityFor("registered")
	// This identity is a helper-level test identity, NOT a core/service receipt.
	var mirrors hoststorage.Assignment
	if err = control.Run(t.Context(), func() error { var e error; mirrors, e = p.ApplyRequested("shared", "mirrors", 0); return e }); err != nil {
		t.Fatal(err)
	}
	members := []payloadbootstrap.Member{}
	for _, name := range []string{"api", "web"} {
		remote := "https://example.invalid/" + name + ".git"
		mirror := repository.Path(p.Root, remote)
		if err = os.Mkdir(mirror, 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.Chown(mirror, int(private.DaemonUID), int(private.DaemonGID)); err != nil {
			t.Fatal(err)
		}
		daemonLowerGit(t, mirror, "init", "--bare", "--initial-branch=main")
		daemonLowerGit(t, mirror, "config", "remote.origin.url", remote)
		blob := strings.TrimSpace(daemonLowerGitInput(t, mirror, "source-"+name+"\n", "hash-object", "-w", "--stdin"))
		link := strings.TrimSpace(daemonLowerGitInput(t, mirror, "payload.txt", "hash-object", "-w", "--stdin"))
		tree := strings.TrimSpace(daemonLowerGitInput(t, mirror, fmt.Sprintf("100644 blob %s\tpayload.txt\n120000 blob %s\tlink\n", blob, link), "mktree"))
		commit := strings.TrimSpace(daemonLowerGit(t, mirror, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit-tree", tree, "-m", "fixture source"))
		daemonLowerGit(t, mirror, "update-ref", "refs/heads/main", commit)
		members = append(members, payloadbootstrap.Member{Name: name, Repository: remote, Branch: "task/" + name, Source: commit, ObjectFormat: "sha1"})
	}
	_ = mirrors
	spec := payloadbootstrap.Spec{Version: payloadbootstrap.Version, Key: "registered", ID: lowerHash("bid:" + string(marker))[:32], CheckpointRepository: "api", QuotaBytes: 32 << 20, Members: members}
	// A legitimate existing allocation is a separate no-adoption refusal.
	var existing hoststorage.Assignment
	if err = control.Run(t.Context(), func() error { var e error; existing, e = p.ApplyRequested("bench", "existing", 32<<20); return e }); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(existing.Path, "owned-sentinel")
	if err = os.WriteFile(sentinel, []byte("existing retained"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(p.Ledger, "assignments.json"))
	if err != nil {
		t.Fatal(err)
	}
	bad := spec
	bad.Key = "existing"
	out, stderr, exit := lowerBootstrap(t, base, helper, expectedSHA, identityFor("existing"), bad)
	if exit == nil || len(out) != 0 || !strings.Contains(stderr, hoststorage.ErrFreshAllocation.Error()) {
		t.Fatalf("existing allocation not precisely refused: %q %q %v", out, stderr, exit)
	}
	assertLowerBytes(t, sentinel, []byte("existing retained"))
	assertLowerBytes(t, filepath.Join(p.Ledger, "assignments.json"), before)
	t.Log("LOWER_ACTUAL_MANAGER_EXISTING_ALLOCATION_NO_ADOPTION_PASS")
	out, stderr, exit = lowerBootstrap(t, base, helper, expectedSHA, identity, spec)
	if exit != nil {
		t.Fatalf("actual materializer failed: %s %v", stderr, exit)
	}
	result, err := payloadbootstrap.ParseResult(out, private, spec)
	if err != nil {
		t.Fatalf("completed bootstrap result invalid: %+v %v", result, err)
	}
	actualStorage, err := p.VerifyStorage(t.Context(), spec.Key)
	if err != nil || result.Allocation != actualStorage.Assignment || result.Allocation.ProjectID < 10000 || result.Allocation.Bytes != spec.QuotaBytes || result.Allocation.Inodes != p.BenchInodes {
		t.Fatalf("bootstrap result differs from actual ledger/kernel assignment: %+v %+v %v", result.Allocation, actualStorage.Assignment, err)
	}
	layoutRaw, err := result.Layout.Bytes(private)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("LOWER_ACTUAL_BOOTSTRAP_RESULT_MATCHES_LEDGER_AND_KERNEL_PASS")
	root := filepath.Join(p.Root, "benches", spec.Key)
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		s, e := os.Lstat(path)
		if e != nil {
			return e
		}
		st := s.Sys().(*syscall.Stat_t)
		if st.Uid != private.PayloadUID || st.Gid != private.PayloadGID {
			return fmt.Errorf("unexpected transferred owner")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range members {
		assertLowerBytes(t, filepath.Join(root, member.Name, "payload.txt"), []byte("source-"+member.Name+"\n"))
		if target, err := os.Readlink(filepath.Join(root, member.Name, "link")); err != nil || target != "payload.txt" {
			t.Fatalf("source symlink changed: %q %v", target, err)
		}
	}
	assertLowerBytes(t, filepath.Join(private.StateRoot, "payload-layouts", spec.Key+".json"), layoutRaw)
	if err = control.Run(t.Context(), func() error { _, e := p.ApplyRequested("bench", spec.Key, 0); return e }); !errors.Is(err, hoststorage.ErrRegisteredAllocation) {
		t.Fatalf("legacy quota path reclaimed registered allocation: %v", err)
	}
	if got := mustLowerInfo(t, root).Sys().(*syscall.Stat_t).Uid; got != private.PayloadUID {
		t.Fatal("legacy path changed payload ownership")
	}
	beforeRelease, err := os.ReadFile(filepath.Join(p.Ledger, "assignments.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = control.Run(t.Context(), func() error { _, err := p.Release(spec.Key, result.Allocation.ProjectID); return err })
	if !errors.Is(err, hoststorage.ErrRegisteredAllocation) {
		t.Fatalf("legacy release bypassed Fresh allocation custody: %v", err)
	}
	assertLowerBytes(t, filepath.Join(p.Ledger, "assignments.json"), beforeRelease)
	if got := mustLowerInfo(t, root).Sys().(*syscall.Stat_t).Uid; got != private.PayloadUID {
		t.Fatal("legacy release changed payload ownership")
	}
	t.Log("LOWER_LEGACY_RELEASE_FRESH_REFUSAL_PASS")
	var ordinary hoststorage.Assignment
	err = control.Run(t.Context(), func() error {
		var err error
		ordinary, err = p.ApplyRequested("bench", "ordinary-release", 4<<20)
		return err
	})
	if err != nil {
		t.Fatal("ordinary allocation setup", err)
	}
	err = control.Run(t.Context(), func() error {
		released, err := p.Release("ordinary-release", ordinary.ProjectID)
		if err == nil && (released.Path != ordinary.Path || released.ProjectID != ordinary.ProjectID || released.Bytes != 0 || released.Inodes != 0) {
			return errors.New("ordinary release changed identity or retained caps")
		}
		return err
	})
	if err != nil {
		t.Fatal("ordinary release compatibility", err)
	}
	if _, err := os.Lstat(ordinary.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("ordinary release retained leaf", err)
	}
	currentReleaseLedger, err := os.ReadFile(filepath.Join(p.Ledger, "assignments.json"))
	if err != nil {
		t.Fatal(err)
	}
	var currentReleaseRows struct {
		Assignments map[string]hoststorage.Assignment
	}
	if err := json.Unmarshal(currentReleaseLedger, &currentReleaseRows); err != nil || currentReleaseRows.Assignments["bench:"+spec.Key] != result.Allocation {
		t.Fatal("ordinary release changed Fresh reservation", err)
	}
	if got := mustLowerInfo(t, root).Sys().(*syscall.Stat_t).Uid; got != private.PayloadUID {
		t.Fatal("ordinary release changed Fresh ownership")
	}
	t.Log("LOWER_LEGACY_RELEASE_ORDINARY_COMPATIBILITY_PASS")

	t.Log("LOWER_ACTUAL_MATERIALIZER_COMPLETE_AND_LEGACY_EXCLUSION_PASS")
	assertLowerRetainedInspection(t, control, p, spec, identity, result, helper, expectedSHA)
	assertFreshAbsentRetirement(t, control, p)
	// A separately admitted identity denied BEFORE Begin must have zero quota,
	// checkout and registration effects through the same actual manager path.
	denied := identityFor("denied")
	store, err := control.Operation(t.Context(), denied, func(i quotaoperation.Identity) error {
		a, e := p.OperationAuthority(t.Context())
		if e != nil {
			return e
		}
		if i != denied || a != i.Authority {
			return quotaoperation.ErrAuthority
		}
		verifyLowerHelper(t, helper, expectedSHA)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Cancel(t.Context()); err != nil {
		t.Fatal(err)
	}
	before, err = os.ReadFile(filepath.Join(p.Ledger, "assignments.json"))
	if err != nil {
		t.Fatal(err)
	}
	bad = spec
	bad.Key = "denied"
	bad.ID = lowerHash("denied-bid:" + string(marker))[:32]
	out, stderr, exit = lowerBootstrap(t, base, helper, expectedSHA, denied, bad)
	if exit == nil || len(out) != 0 || !strings.Contains(stderr, quotaoperation.ErrCancelled.Error()) {
		t.Fatalf("denied operation not precisely refused: %q %q %v", out, stderr, exit)
	}
	assertLowerBytes(t, filepath.Join(p.Ledger, "assignments.json"), before)
	for _, path := range []string{filepath.Join(p.Root, "benches", bad.Key), filepath.Join(private.StateRoot, "payload-layouts", bad.Key+".json")} {
		if _, e := os.Lstat(path); !errors.Is(e, os.ErrNotExist) {
			t.Fatalf("denied helper had effects: %s %v", path, e)
		}
	}
	t.Log("LOWER_ACTUAL_MANAGER_DENIAL_BEFORE_EFFECTS_PASS")
	t.Log("LOWER_SCOPE_ONLY_NO_BOOTED_SYSTEMD_GATE_OR_INSTALL_LIFECYCLE_CLAIM")
}

func lowerHash(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func mustLowerInfo(t *testing.T, path string) os.FileInfo {
	t.Helper()
	s, e := os.Lstat(path)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func assertLowerBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, e := os.ReadFile(path)
	if e != nil || !bytes.Equal(got, want) {
		t.Fatalf("retained evidence differs: %s %v", path, e)
	}
}
func verifyLowerHelper(t *testing.T, path, want string) {
	t.Helper()
	if len(want) != 64 || storagecustody.TrustedAncestors(path) != nil {
		t.Fatal("helper custody missing")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	st := before.Sys().(*syscall.Stat_t)
	if !before.Mode().IsRegular() || before.Mode().Perm() != 0755 || st.Uid != 0 || st.Nlink != 1 || before.Size() > 128<<20 {
		t.Fatal("helper custody invalid")
	}
	h := sha256.New()
	if _, err = io.Copy(h, io.LimitReader(f, (128<<20)+1)); err != nil {
		t.Fatal(err)
	}
	after, err := f.Stat()
	named, e := os.Lstat(path)
	if err != nil || e != nil || !os.SameFile(before, after) || !os.SameFile(before, named) || before.Size() != after.Size() || before.ModTime() != after.ModTime() || hex.EncodeToString(h.Sum(nil)) != want {
		t.Fatal("helper pin differs")
	}
}
func daemonLowerGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return daemonLowerGitInput(t, dir, "", args...)
}
func daemonLowerGitInput(t *testing.T, dir, input string, args ...string) string {
	t.Helper()
	cmd := exec.Command("/usr/bin/git", args...)
	cmd.Stdin = strings.NewReader(input)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 61001, Gid: 61003, Groups: []uint32{}}}
	out, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("owned source Git failed: %v %s", e, out)
	}
	return string(out)
}

// Direct actual manager Bootstrap API only; this does not test the command or
// service route. SelfBinarySHA binds the static fixture actually calling it.
func lowerBootstrap(t *testing.T, base, helper, sha string, id quotaoperation.Identity, spec payloadbootstrap.Spec) ([]byte, string, error) {
	t.Helper()
	verifyLowerHelper(t, helper, sha)
	control, _, err := quotaoperationcontrol.Open(t.Context())
	if err != nil {
		return nil, err.Error(), err
	}
	manager, err := payloadbootstrap.Open(t.Context(), control, id)
	if err != nil {
		return nil, err.Error(), err
	}
	result, err := manager.Bootstrap(t.Context(), spec)
	if err != nil {
		return nil, err.Error(), err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	verifyLowerHelper(t, helper, sha)
	return raw, "", nil
}

// This is a lower fixture constructor, not the production installation gate.
// The supervisor supplies a freshly formatted owned XFS device; the real
// initializer capability and OperationAuthority still control publication.
func initializeLowerRecords(t *testing.T, p hoststorage.Policy) (*quotaoperationjournal.Control, func(), error) {
	t.Helper()
	root, err := os.OpenFile(p.Root, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	before, err := root.Stat()
	if err != nil {
		root.Close()
		return nil, nil, err
	}
	fail := func(e error) (*quotaoperationjournal.Control, func(), error) { root.Close(); return nil, nil, e }
	var fs syscall.Statfs_t
	if err = syscall.Fstatfs(int(root.Fd()), &fs); err != nil || fs.Type != 0x58465342 {
		return fail(errors.New("lower fixture requires actual XFS"))
	}
	parent, err := os.Stat(filepath.Dir(p.Root))
	if err != nil || parent.Sys().(*syscall.Stat_t).Dev == before.Sys().(*syscall.Stat_t).Dev {
		return fail(errors.New("lower fixture requires dedicated mount"))
	}
	mount := exec.CommandContext(t.Context(), "/usr/bin/findmnt", "--noheadings", "--output", "UUID,FSROOT", "--target", p.Root)
	mount.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	raw, err := mount.Output()
	fields := strings.Fields(string(raw))
	if err != nil || len(fields) != 2 || fields[1] != "/" {
		return fail(errors.New("lower mount identity unavailable"))
	}
	uuid := strings.ToLower(fields[0])
	check := func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		held, err := root.Stat()
		if err != nil {
			return err
		}
		named, err := os.Lstat(p.Root)
		if err != nil || !os.SameFile(before, held) || !os.SameFile(before, named) || named.Mode() != before.Mode() {
			return errors.New("lower physical mount changed")
		}
		current, err := hoststorage.Load("/etc/superbench/storage.json")
		if err != nil || !reflect.DeepEqual(current, p) {
			return errors.New("lower fixed storage policy changed")
		}
		return storagecustody.RootOwnedDirectory(filepath.Dir(p.Ledger), true)
	}
	authorize := func(ctx context.Context) error {
		if err := check(ctx); err != nil {
			return err
		}
		if _, err := root.Seek(0, io.SeekStart); err != nil {
			return err
		}
		names, err := root.Readdirnames(1)
		if len(names) != 0 || !errors.Is(err, io.EOF) {
			return errors.New("lower fixture storage is not empty")
		}
		if _, err := os.Lstat(p.Ledger); !errors.Is(err, syscall.ENOENT) {
			return errors.New("lower fixture ledger already exists")
		}
		return nil
	}
	initializer, err := quotaoperationjournal.NewInitializer(quotaoperationjournal.FreshConfig{
		Control: quotaoperationjournal.Config{Directory: p.Ledger, CheckControl: check, CheckReady: p.OperationAuthority}, Authorize: authorize})
	if err != nil {
		return fail(err)
	}
	control, err := initializer.InitializeFresh(t.Context(), func(ctx context.Context, fresh quotaoperationjournal.FreshLedger) error {
		if err := check(ctx); err != nil {
			return err
		}
		header, anchor, err := p.FreshAllocationRecords(fresh, uuid)
		if err != nil {
			return err
		}
		// A real capability cannot encode records for another physical ledger.
		other := p
		other.Ledger = filepath.Join(filepath.Dir(p.Ledger), "other-fixture-ledger")
		if err = os.Mkdir(other.Ledger, 0700); err != nil {
			return err
		}
		if _, _, err = other.FreshAllocationRecords(fresh, uuid); err == nil || err.Error() != "fresh ledger capability differs from policy" {
			return errors.New("foreign ledger accepted fresh capability")
		}
		for _, record := range []struct {
			path string
			raw  []byte
		}{{filepath.Join(p.Ledger, "assignments.json"), header}, {filepath.Join(p.Root, ".superbench-quota-owner.json"), anchor}} {
			output, err := os.OpenFile(record.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
			if err != nil {
				return err
			}
			_, err = output.Write(record.raw)
			if err == nil {
				err = output.Sync()
			}
			closeErr := output.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
			directory, err := os.Open(filepath.Dir(record.path))
			if err != nil {
				return err
			}
			err = directory.Sync()
			directory.Close()
			if err != nil {
				return err
			}
		}
		t.Log("LOWER_REAL_FRESH_CAPABILITY_FOREIGN_LEDGER_REFUSED")
		return check(ctx)
	})
	if err != nil {
		return fail(err)
	}
	return control, func() { _ = root.Close() }, nil
}
