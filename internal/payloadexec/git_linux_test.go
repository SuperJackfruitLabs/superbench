//go:build linux

package payloadexec

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitpolicy"
)

func TestHelperRejectsGitWithoutPolicyOrWithLeadingOverride(t *testing.T) {
	for _, args := range [][]string{{"git", "status"}, {"git", "-c", "core.hooksPath=/tmp/hooks", "status"}, gitpolicy.Arguments("-c", "core.fsmonitor=unsafe", "status")} {
		r := launchRequest()
		r.Kind = "git"
		r.Arguments = args
		// Marshal directly: exercise the helper parser rather than the caller builder.
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = ParseRequest(raw); !errors.Is(err, ErrRequest) {
			t.Fatalf("helper accepted override: %q %v", args, err)
		}
	}
}

func TestManagedGitConfigAndAlternatesRefuseBeforeCommand(t *testing.T) {
	for _, kind := range []string{"clean", "filter-clean", "filter-process", "filter-smudge", "diff-driver", "textconv", "alias", "alternates", "http-alternates", "foreign-admin"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			checkout, admin := filepath.Join(root, "checkout"), filepath.Join(root, "admin")
			must := func(args ...string) {
				t.Helper()
				c := exec.Command("/usr/bin/git", args...)
				c.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + root, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
				if out, err := c.CombinedOutput(); err != nil {
					t.Fatalf("fixture Git: %v %s", err, out)
				}
			}
			must("init", "--separate-git-dir="+admin, checkout)
			switch kind {
			case "filter-clean":
				must("-C", checkout, "config", "filter.fixture.clean", "touch forbidden")
			case "filter-process":
				must("-C", checkout, "config", "filter.fixture.process", "touch forbidden")
			case "filter-smudge":
				must("-C", checkout, "config", "filter.fixture.smudge", "touch forbidden")
			case "diff-driver":
				must("-C", checkout, "config", "diff.fixture.command", "touch forbidden")
			case "textconv":
				must("-C", checkout, "config", "diff.fixture.textconv", "touch forbidden")
			case "alias":
				must("-C", checkout, "config", "alias.fixture", "!touch forbidden")
			case "alternates", "http-alternates":
				if err := os.WriteFile(filepath.Join(admin, "objects", "info", kind), nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "foreign-admin":
				admin = root
			}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(exe, "-test.run=^TestManagedGitPolicyChild$")
			cmd.Dir = checkout
			cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + root, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "MANAGED_GIT_POLICY_CHILD=" + kind, "MANAGED_GIT_ADMIN=" + admin}
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("policy child: %v %s", err, out)
			}
			if _, err := os.Stat(filepath.Join(checkout, "forbidden")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("driver executed: %v", err)
			}
		})
	}
}

func TestManagedGitPolicyChild(t *testing.T) {
	kind := os.Getenv("MANAGED_GIT_POLICY_CHILD")
	if kind == "" {
		return
	}
	admin, err := os.Open(os.Getenv("MANAGED_GIT_ADMIN"))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	r := launchRequest()
	r.Kind = "git"
	r.Arguments = gitpolicy.Arguments("status", "--porcelain")
	err = verifyManagedGit(r, admin, os.Environ())
	if kind == "clean" {
		if err != nil {
			t.Fatalf("valid Git refused: %v", err)
		}
	} else if !errors.Is(err, ErrBoundary) {
		t.Fatalf("unsafe Git config/admin admitted: %v", err)
	}
}
