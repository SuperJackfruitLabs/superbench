//go:build linux && serviceidentityfixture

package serviceidentity

// This opt-in fixture must be compiled CGO_ENABLED=0 and run by an outer
// root supervisor in its own mount+network namespaces and bounded cgroup.
// It never changes host installation files or creates accounts. The outer
// supervisor remains responsible for killing its entire owned cgroup on error.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
)

const fixtureUID = 61001
const fixtureGID = 61003

func fixtureNamespace(t *testing.T, kind, expected string) {
	t.Helper()
	// Require the actual outer namespace identity, not an arbitrary unequal
	// string. This fixture deliberately does not enter a separate PID namespace.
	initial, err := os.Readlink("/proc/1/ns/" + kind)
	if err != nil || expected != initial || !strings.HasPrefix(initial, kind+":[") {
		t.Fatalf("missing/incorrect original %s namespace: %v", kind, err)
	}
	current, err := os.Readlink("/proc/self/ns/" + kind)
	if err != nil || current == initial || !strings.HasPrefix(current, kind+":[") {
		t.Fatalf("fixture requires private %s namespace: %v", kind, err)
	}
	var originalStat, currentStat syscall.Stat_t
	if syscall.Stat("/proc/1/ns/"+kind, &originalStat) != nil || syscall.Stat("/proc/self/ns/"+kind, &currentStat) != nil || originalStat.Dev == currentStat.Dev && originalStat.Ino == currentStat.Ino {
		t.Fatalf("%s namespace device/inode separation not proven", kind)
	}
}

func TestInstalledServiceIdentityFixture(t *testing.T) {
	if os.Getuid() != 0 || os.Geteuid() != 0 || os.Getenv("SUPERBENCH_IDENTITY_FIXTURE") != "1" {
		t.Fatal("explicit root fixture invocation required")
	}
	// ALL checks below precede mkdir, mount or credential-changing children.
	selfPID, err := os.Readlink("/proc/self/ns/pid")
	initialPID, initialErr := os.Readlink("/proc/1/ns/pid")
	if err != nil || initialErr != nil || selfPID != initialPID {
		t.Fatal("fixture requires original PID namespace for independent namespace comparison")
	}
	fixtureNamespace(t, "mnt", os.Getenv("SUPERBENCH_IDENTITY_ORIGINAL_MNT"))
	fixtureNamespace(t, "net", os.Getenv("SUPERBENCH_IDENTITY_ORIGINAL_NET"))
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("/var/tmp", "superbench-identity-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	// No recursive cleanup. The outer supervisor preserves unknown leftovers.
	defer func() {
		if err := os.Remove(root); err != nil {
			t.Errorf("fixture directory retained: %v", err)
		}
	}()
	etc := filepath.Join(root, "etc")
	if err := os.Mkdir(etc, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(etc, 0755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mount(etc, "/etc", "", syscall.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := syscall.Unmount("/etc", 0); err != nil {
			t.Errorf("private /etc mount retained: %v", err)
			return
		}
		if err := os.Remove(etc); err != nil {
			t.Errorf("private etc evidence retained: %v", err)
		}
	}()
	p := payloadpolicy.Policy{DaemonUID: fixtureUID, DaemonGID: fixtureGID, PayloadUID: 61002, PayloadGID: 61004, DataRoot: "/data", StateRoot: "/state", CgroupRoot: "/cgroup"}
	r, err := ForPolicy(p)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"positive", "leaf", "ancestor", "credentials", "wrong-gid"} {
		if !t.Run(scenario, func(t *testing.T) { runInstalledScenario(t, raw, scenario) }) {
			// Preserve the failed fixture; do not reuse contaminated paths.
			return
		}
	}
}

func writeFixtureRegistry(t *testing.T, path string, raw []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Chmod(0644); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// readFixtureLine is bounded even if a broken child emits unbounded output.
// CommandContext supplies the time bound and closes the pipe by killing child.
func readFixtureLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadSlice('\n')
	if err != nil || len(line) > 128 {
		return "", fmt.Errorf("invalid bounded fixture response: %w", err)
	}
	return string(line[:len(line)-1]), nil
}

func runInstalledScenario(t *testing.T, raw []byte, scenario string) {
	t.Helper()
	dir := filepath.Dir(FixedRegistry)
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFixtureRegistry(t, FixedRegistry, raw)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestInstalledServiceIdentityProbe$")
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "SUPERBENCH_IDENTITY_PROBE=" + scenario}
	if scenario != "credentials" {
		gid := uint32(fixtureGID)
		if scenario == "wrong-gid" {
			gid++
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: fixtureUID, Gid: gid, Groups: []uint32{}}}
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	finished := false
	defer func() {
		_ = input.Close()
		if !finished {
			cancel()
			_ = cmd.Wait()
		}
	}()
	reader := bufio.NewReaderSize(output, 256)
	want := "RESOLVED"
	if scenario == "wrong-gid" {
		want = "REGISTERED_UID_WRONG_GID_REFUSED"
	}
	if got, err := readFixtureLine(reader); err != nil || got != want {
		t.Fatalf("probe initial response %q: %v", got, err)
	}
	if scenario == "leaf" {
		before, err := os.Lstat(FixedRegistry)
		if err != nil {
			t.Fatal(err)
		}
		replacement := filepath.Join(dir, "replacement")
		writeFixtureRegistry(t, replacement, raw)
		if err := os.Rename(replacement, FixedRegistry); err != nil {
			t.Fatal(err)
		}
		after, err := os.Lstat(FixedRegistry)
		if err != nil || os.SameFile(before, after) {
			t.Fatal("leaf replacement identity not established")
		}
	}
	oldDir := dir + ".retained"
	leafMounted := false
	if scenario == "ancestor" {
		var before, after syscall.Stat_t
		if err := syscall.Lstat(FixedRegistry, &before); err != nil {
			t.Fatal(err)
		}
		oldParent, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(dir, oldDir); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0755); err != nil {
			t.Fatal(err)
		}
		writeFixtureRegistry(t, FixedRegistry, raw)
		if err := syscall.Mount(filepath.Join(oldDir, filepath.Base(FixedRegistry)), FixedRegistry, "", syscall.MS_BIND, ""); err != nil {
			t.Fatal(err)
		}
		leafMounted = true
		defer func() {
			if leafMounted {
				if err := syscall.Unmount(FixedRegistry, 0); err != nil {
					t.Errorf("leaf bind mount retained: %v", err)
				}
			}
		}()
		newParent, err := os.Stat(dir)
		if err != nil || os.SameFile(oldParent, newParent) || syscall.Stat(FixedRegistry, &after) != nil || fileID(&before) != fileID(&after) {
			t.Fatal("ancestor-only replacement did not preserve exact original leaf identity")
		}
	}
	finalMarker := want
	if scenario != "wrong-gid" {
		if _, err := io.WriteString(input, "CHECK\n"); err != nil {
			t.Fatal(err)
		}
		markers := map[string]string{"positive": "ACTUAL_DAEMON_RESOLVE_OK", "leaf": "IDENTICAL_LEAF_REPLACEMENT_REFUSED", "ancestor": "ANCESTOR_REPLACEMENT_REFUSED", "credentials": "ACTUAL_CREDENTIAL_CHANGE_REFUSED"}
		finalMarker = markers[scenario]
		if got, err := readFixtureLine(reader); err != nil || got != finalMarker {
			t.Fatalf("probe final response %q: %v", got, err)
		}
	}
	_ = input.Close()
	// Consume only the Go test runner's bounded success trailer. Drain before
	// Wait, as required by StdoutPipe; unexpected output is a fixture failure.
	trailer, readErr := io.ReadAll(io.LimitReader(reader, 129))
	waitErr := cmd.Wait()
	finished = true
	if readErr != nil || string(trailer) != "PASS\n" || waitErr != nil || ctx.Err() != nil || stderr.Len() != 0 {
		t.Fatalf("probe completion: trailer=%q read=%v wait=%v ctx=%v stderr=%q", trailer, readErr, waitErr, ctx.Err(), stderr.String())
	}
	// Only explicit known fixture entries are removed; preserve failures.
	if scenario == "ancestor" {
		if err := syscall.Unmount(FixedRegistry, 0); err != nil {
			t.Fatal(err)
		}
		leafMounted = false
		if err := os.Remove(filepath.Join(oldDir, filepath.Base(FixedRegistry))); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(oldDir); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(FixedRegistry); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	t.Log("PROOF " + finalMarker + " scenario=" + scenario)
}

func TestInstalledServiceIdentityProbe(t *testing.T) {
	scenario := os.Getenv("SUPERBENCH_IDENTITY_PROBE")
	if scenario == "" {
		return
	}
	if scenario == "credentials" {
		// Test-only saved-root IDs permit a real credential transition AFTER
		// resolution. Normal daemon children are irreversibly dropped by exec.
		if os.Getuid() != 0 || os.Geteuid() != 0 || syscall.Setgroups(nil) != nil || syscall.Setresgid(fixtureGID, fixtureGID, 0) != nil || syscall.Setresuid(fixtureUID, fixtureUID, 0) != nil {
			t.Fatal("test-only saved-ID setup failed")
		}
	}
	if os.Getuid() != fixtureUID || os.Geteuid() != fixtureUID || os.Getgid() != fixtureGID && scenario != "wrong-gid" || os.Getegid() != os.Getgid() {
		t.Fatal("actual daemon credential precondition failed")
	}
	a, err := ResolveService()
	if scenario == "wrong-gid" {
		if !errors.Is(err, ErrAuthority) || a.physical != nil {
			t.Fatal("wrong GID did not refuse precisely")
		}
		fmt.Println("REGISTERED_UID_WRONG_GID_REFUSED")
		return
	}
	if err != nil || a.Recheck() != nil {
		t.Fatalf("actual daemon resolution failed: %v", err)
	}
	p, err := a.Policy()
	if err != nil || p.DaemonUID != fixtureUID || p.DaemonGID != fixtureGID {
		t.Fatal("policy binding failed")
	}
	fmt.Println("RESOLVED")
	line, err := readFixtureLine(bufio.NewReaderSize(os.Stdin, 256))
	if err != nil || line != "CHECK" {
		t.Fatal("bounded root coordinator protocol failed")
	}
	if scenario == "positive" {
		if err := a.Recheck(); err != nil {
			t.Fatal(err)
		}
		fmt.Println("ACTUAL_DAEMON_RESOLVE_OK")
		return
	}
	if scenario == "credentials" {
		if err := syscall.Setresuid(-1, 0, -1); err != nil || os.Getuid() != fixtureUID || os.Geteuid() != 0 {
			t.Fatal("actual effective UID change failed: " + strconv.Itoa(os.Geteuid()))
		}
	}
	for i := 0; i < 2; i++ {
		if !errors.Is(a.Recheck(), ErrAuthority) {
			t.Fatal("old authority was accepted/rebound")
		}
		if got, err := a.Policy(); !errors.Is(err, ErrAuthority) || got != (payloadpolicy.Policy{}) {
			t.Fatal("old authority returned policy")
		}
	}
	if scenario == "credentials" {
		fmt.Println("ACTUAL_CREDENTIAL_CHANGE_REFUSED")
		return
	}
	fresh, err := ResolveService()
	if err != nil || fresh.Recheck() != nil {
		t.Fatal("valid replacement does not allow an explicit fresh binding")
	}
	if !errors.Is(a.Recheck(), ErrAuthority) {
		t.Fatal("fresh resolution revived the original authority")
	}
	if scenario == "leaf" {
		fmt.Println("IDENTICAL_LEAF_REPLACEMENT_REFUSED")
	} else if scenario == "ancestor" {
		fmt.Println("ANCESTOR_REPLACEMENT_REFUSED")
	} else {
		t.Fatal("unknown fixture scenario")
	}
}
