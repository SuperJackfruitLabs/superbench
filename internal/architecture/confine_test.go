package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The privileged routes these pins guard cannot run unprivileged; the lower XFS
// fixture proves them. These pins keep the wiring from being dropped silently.
func TestPayloadConfinementAndLeafChecksStayWired(t *testing.T) {
	root := filepath.Join("..", "..")
	read := func(path string) string {
		raw, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	// The one payload-uid transition installs the filter after the capability
	// drop and before its final proof, which requires seccomp filter mode.
	drop := read("internal/payloadexec/credentials_linux.go")
	body := drop[strings.Index(drop, "func dropCredentials("):strings.Index(drop, "func verifyDropped(")]
	install, capset, proof := strings.Index(body, "payloadconfine.Install()"), strings.Index(body, "SYS_CAPSET"), strings.Index(body, "return verifyDropped(p)")
	if install < 0 || capset < 0 || proof < 0 || !(capset < install && install < proof) {
		t.Fatal("dropCredentials must install payload confinement after the capability drop and before its proof")
	}
	if !strings.Contains(drop, `"Seccomp:": "2"`) {
		t.Fatal("verifyDropped must require seccomp filter mode")
	}
	if n := strings.Count(read("internal/payloadexec/exec_linux.go"), "dropCredentials("); n != 1 {
		t.Fatalf("Exec must reach the payload uid only through dropCredentials, found %d calls", n)
	}
	// No other production code may start a process as the payload account.
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if rel == "internal/payloadexec/credentials_linux.go" || strings.HasPrefix(rel, "internal/confinefixture/") {
			return nil // the confined transition, and the fixture's unconfined control
		}
		for _, line := range strings.Split(read(rel), "\n") {
			if strings.Contains(line, "PayloadUID") && (strings.Contains(line, "Credential{") || strings.Contains(line, "Setresuid(") || strings.Contains(line, "Setuid(")) {
				t.Errorf("%s starts a payload-uid process outside the confined drop: %s", rel, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read("internal/hoststorage/verify_linux.go"), "quotaleaf.Leaf(benches, key, a.ProjectID)") {
		t.Fatal("root's read-only verify must check the bench leaf's project and inherit flag")
	}
	if !strings.Contains(read("internal/workspace/lifecycle.go"), "l.FreshQuota(committed); err != nil") {
		t.Fatal("checkpoint export must verify managed storage first")
	}
}
