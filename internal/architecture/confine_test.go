package architecture

import (
	"os"
	"path/filepath"
	"regexp"
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
	// Only these files may switch credentials: set a Credential or call any
	// set*uid/set*gid. Each is either confined (its payload-uid processes run
	// under the payload filter, proven below) or a reviewed exception.
	switches := map[string]string{
		"internal/payloadexec/credentials_linux.go":       "confined: the payload drop installs the filter itself",
		"internal/freshrecovery/worker_linux.go":          "confined: the clean worker it spawns, the purge as the payload account included, installs the filter first",
		"internal/payloadbootstrap/git_linux.go":          "exception: Git as the daemon account during bootstrap, never the payload",
		"internal/lowerfixture/lowerfixture_linux.go":     "exception: fixture-only daemon-account Git, tag freshstoragefixture",
		"internal/confinefixture/confine_linux.go":        "exception: fixture-only unconfined control proving the escape is real",
		"internal/freshrecovery/fixture/reclaim_linux.go": "exception: fixture-only unconfined payload writes and an in-use process",
	}
	pattern := regexp.MustCompile(`Credential\{|\bSet(res|re|fs)?[ug]id\(|SYS_SET(RES|RE|FS)?[UG]ID`)
	seen := map[string]bool{}
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			for _, line := range strings.Split(read(rel), "\n") {
				if pattern.MatchString(line) {
					if _, ok := switches[rel]; !ok {
						t.Errorf("%s switches credentials outside the reviewed routes: %s", rel, strings.TrimSpace(line))
					}
					seen[rel] = true
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for rel := range switches {
		if !seen[rel] {
			t.Errorf("%s no longer switches credentials; drop it from the allow-list", rel)
		}
	}
	// The clean worker spawn re-executes bench-quota as the worker, which
	// installs the filter right after no_new_privs, before reading anything,
	// and proves filter mode in its status self-check.
	if spawn := read("internal/freshrecovery/worker_linux.go"); !strings.Contains(spawn, `exec.CommandContext(ctx, "/proc/self/exe")`) || !strings.Contains(spawn, "cleanworker.Command") {
		t.Fatal("the clean worker spawn must re-execute bench-quota as the clean worker")
	}
	worker := read("internal/cleanworker/worker_linux.go")
	isolate := worker[strings.Index(worker, "func isolate() error {"):]
	isolate = isolate[:strings.Index(isolate, "\n}\n")]
	nnp, confine := strings.Index(isolate, "noNewPrivs()"), strings.Index(isolate, "payloadconfine.Install()")
	if nnp < 0 || confine < nnp {
		t.Fatal("the clean worker must install payload confinement after no_new_privs")
	}
	run := worker[strings.Index(worker, "func run("):]
	if first := strings.Index(run, "if err := isolate(); err != nil"); first < 0 || strings.Contains(run[:first], "parseArgs(") {
		t.Fatal("the clean worker must isolate itself before anything else")
	}
	if !strings.Contains(read("internal/cleanworker/selfcheck.go"), `"NoNewPrivs:": "1", "Seccomp:": "2"}`) {
		t.Fatal("the clean worker's status self-check must require seccomp filter mode")
	}
	// Reclaim walks the tree as root before retention and the purge (idle),
	// and checks the emptied leaf before removing it.
	reclaim := read("internal/freshrecovery/reclaim_linux.go")
	if !strings.Contains(reclaim, "return c.contained()") || !strings.Contains(reclaim, "if err = c.treeContained(leaf); err != nil") || !strings.Contains(reclaim, "quotaleaf.Tree(leaf, c.assignment.ProjectID, int(c.assignment.Inodes))") {
		t.Fatal("reclaim must check the whole tree's project before the purge and the removal")
	}
	removal := reclaim[strings.Index(reclaim, "func (c *reclaiming) remove("):]
	if strings.Index(removal, "c.treeContained(leaf)") > strings.Index(removal, "rmdirAt(") {
		t.Fatal("the emptied leaf must be checked before it is removed")
	}
	if !strings.Contains(read("internal/hoststorage/verify_linux.go"), "quotaleaf.Leaf(benches, key, a.ProjectID)") {
		t.Fatal("root's read-only verify must check the bench leaf's project and inherit flag")
	}
	if !strings.Contains(read("internal/workspace/lifecycle.go"), "l.FreshQuota(committed); err != nil") {
		t.Fatal("checkpoint export must verify managed storage first")
	}
}
