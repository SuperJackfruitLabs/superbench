//go:build linux && (executioncredentialfixture || serviceidentityfixture)

package execution

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

type credentialProbe struct {
	UID, EUID, GID, EGID int
	Groups               []int
}

func TestExecutionCredentialChildProbe(t *testing.T) {
	if os.Getenv("SUPERBENCH_EXECUTION_CREDENTIAL_PROBE") != "1" {
		t.Fatal("credential probe requires its explicit fixture parent")
	}
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(credentialProbe{os.Getuid(), os.Geteuid(), os.Getgid(), os.Getegid(), groups})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("EXECUTION_CREDENTIAL_PROBE=%s\n", raw)
}

// This opt-in fixture requires a bounded, owned disposable cgroup. It creates
// only its random child scope; it never installs users or changes host policy.
func TestHostStartPreservesActualChildCredentials(t *testing.T) {
	if os.Getenv("SUPERBENCH_EXECUTION_CREDENTIAL_FIXTURE") != "1" || os.Getuid() != 0 || os.Geteuid() != 0 {
		t.Fatal("credential fixture requires explicit Linux root invocation")
	}
	root := os.Getenv("SUPERBENCH_EXECUTION_CGROUP_ROOT")
	if filepath.Dir(root) != "/sys/fs/cgroup" || !(strings.HasPrefix(filepath.Base(root), "superbench-execution-credentials-") || strings.HasPrefix(filepath.Base(root), "superbench-identity-ci-")) {
		t.Fatal("credential fixture requires its dedicated cgroup")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 || info.Sys().(*syscall.Stat_t).Uid != 0 {
		t.Fatal("credential fixture cgroup ancestry refused", err)
	}
	for name, expected := range map[string]string{"cpu.max": "800000 100000", "memory.max": "2147483648", "memory.swap.max": "0", "pids.max": "512"} {
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || strings.TrimSpace(string(raw)) != expected {
			t.Fatalf("credential fixture resource boundary refused: %s: %v", name, err)
		}
	}
	procs, err := os.ReadFile(filepath.Join(root, "cgroup.procs"))
	if err != nil || !strings.Contains("\n"+string(procs), "\n"+strconv.Itoa(os.Getpid())+"\n") {
		t.Fatal("credential fixture must run inside its bounded cgroup", err)
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	e := &Execution{Root: root, ID: hex.EncodeToString(random[:])}
	host := Host{}
	identity, err := host.Create(root, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.HostID, e.BootID = identity.HostID, identity.BootID
	defer func() {
		if err := host.Fence(e); err != nil {
			t.Error("credential fixture scope cleanup failed", err)
		}
	}()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-test.run=^TestExecutionCredentialChildProbe$", "-test.v", "-test.timeout=15s")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "SUPERBENCH_EXECUTION_CREDENTIAL_PROBE=1"}
	attributes := &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 61001, Gid: 61003, Groups: []uint32{}}, Pdeathsig: syscall.SIGKILL}
	cmd.SysProcAttr = attributes
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err = host.Start(cmd, e); err != nil {
		t.Fatal("actual scoped credential launch failed", err)
	}
	if err = cmd.Wait(); err != nil {
		t.Fatalf("actual child failed: %v: %s", err, output.String())
	}
	if err = host.Fence(e); err != nil {
		t.Fatal("actual child scope did not fence", err)
	}
	_, encoded, found := strings.Cut(output.String(), "EXECUTION_CREDENTIAL_PROBE=")
	encoded, _, _ = strings.Cut(encoded, "\n")
	var observed credentialProbe
	if !found || json.Unmarshal([]byte(encoded), &observed) != nil || observed.UID != 61001 || observed.EUID != 61001 || observed.GID != 61003 || observed.EGID != 61003 || len(observed.Groups) != 0 || !strings.Contains(output.String(), "--- PASS: TestExecutionCredentialChildProbe") {
		t.Fatalf("credential drop was lost: %s", output.String())
	}
	if cmd.SysProcAttr == attributes || attributes.UseCgroupFD || attributes.CgroupFD != 0 || cmd.SysProcAttr.Pdeathsig != syscall.SIGKILL {
		t.Fatal("scope attachment mutated caller attributes or lost process restrictions")
	}
	t.Log("ACTUAL_SCOPED_CHILD_CREDENTIALS_PRESERVED")
}
