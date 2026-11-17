package serviceidentity

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
)

// The installer writes these documents; the real parsers must accept its exact
// bytes. Ownership and mode are covered by the installer tests and by the
// installed-identity fixtures.
func TestInstallerDocumentsAreCanonical(t *testing.T) {
	installer, err := filepath.Abs(filepath.Join("..", "..", "deploy", "linux"))
	if err != nil {
		t.Fatal(err)
	}
	script := `import sys
sys.path.insert(0, sys.argv[1])
import install
documents = install.identity_documents((977, 977), (976, 976))
sys.stdout.buffer.write(documents[install.POLICY_FILE][0] + b"\n" + documents[install.REGISTRY_FILE][0])`
	out, err := exec.Command("python3", "-I", "-c", script, installer).Output()
	if err != nil {
		t.Fatal(err)
	}
	private, public, ok := bytes.Cut(out, []byte("\n"))
	if !ok {
		t.Fatalf("unexpected installer output %q", out)
	}
	policy, err := payloadpolicy.ParsePolicy(private)
	if err != nil {
		t.Fatal("payload policy refused", err)
	}
	if canonical, _ := policy.Bytes(); !bytes.Equal(canonical, private) {
		t.Fatalf("payload policy is not canonical: %s", private)
	}
	want := payloadpolicy.Policy{DaemonUID: 977, DaemonGID: 977, PayloadUID: 976, PayloadGID: 976,
		DataRoot: "/srv/superbench-data", StateRoot: "/var/lib/superbench/state", CgroupRoot: "/sys/fs/cgroup/system.slice/superbench.service"}
	if policy != want {
		t.Fatal("payload policy fields", policy)
	}
	registry, err := ParseRegistry(public)
	if err != nil {
		t.Fatal("service identity refused", err)
	}
	derived, err := ForPolicy(policy)
	if err != nil || registry != derived {
		t.Fatal("service identity is not derived from the payload policy", registry, err)
	}
	if canonical, _ := derived.Bytes(); !bytes.Equal(canonical, public) {
		t.Fatalf("service identity is not canonical: %s", public)
	}
}
