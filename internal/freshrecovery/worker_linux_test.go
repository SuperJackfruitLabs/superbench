//go:build linux

package freshrecovery

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
)

func TestWorkerRunsAsTheDaemonInItsOwnSessionAndCgroup(t *testing.T) {
	a := workerAttributes(payloadpolicy.Policy{DaemonUID: 61001, DaemonGID: 61003}, 9)
	if a.Credential == nil || a.Credential.Uid != 61001 || a.Credential.Gid != 61003 || a.Credential.Groups == nil || len(a.Credential.Groups) != 0 || a.Credential.NoSetGroups {
		t.Fatalf("credentials: %+v", a.Credential)
	}
	if !a.Setsid || a.Setctty || a.Foreground || a.Pdeathsig != syscall.SIGKILL || !a.UseCgroupFD || a.CgroupFD != 9 || len(a.AmbientCaps) != 0 || a.Chroot != "" {
		t.Fatalf("attributes: %+v", a)
	}
}

// The worker's stderr can name daemon-controlled files: it reaches root only
// quoted, so an escape sequence prints as text.
func TestWorkerDiagnosticsAreQuoted(t *testing.T) {
	err := workerError(errors.New("exit status 1"), []byte("refused: work/\x1b]0;owned\x07\x1b[2J\n"))
	if strings.ContainsAny(err.Error(), "\x1b\x07\n") || !strings.Contains(err.Error(), `\x1b]0;owned\a\x1b[2J`) {
		t.Fatalf("diagnostic not quoted: %q", err.Error())
	}
}

func TestCappedOutputRefusesOverflow(t *testing.T) {
	c := &capped{max: 4}
	if n, err := c.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatal(n, err)
	}
	if _, err := c.Write([]byte("de")); !errors.Is(err, ErrRecovery) || !c.over || string(c.raw) != "abc" {
		t.Fatalf("overflow kept: %q %v", c.raw, err)
	}
}

func TestOwnCgroupIsTheSingleCanonicalV2Path(t *testing.T) {
	if got, err := ownCgroup([]byte("0::/user.slice/user-0.slice/session-3.scope\n")); err != nil || got != "/sys/fs/cgroup/user.slice/user-0.slice/session-3.scope" {
		t.Fatal(got, err)
	}
	if got, err := ownCgroup([]byte("0::/\n")); err != nil || got != "/sys/fs/cgroup" {
		t.Fatal("root cgroup", got, err)
	}
	for name, raw := range map[string]string{
		"v1 only":   "1:name=systemd:/x\n",
		"relative":  "0::user.slice\n",
		"unclean":   "0::/a/../b\n",
		"twice":     "0::/a\n0::/b\n",
		"deleted":   "0::/a (deleted)\n",
		"empty":     "",
		"dot-slash": "0::/a/./b\n",
	} {
		if got, err := ownCgroup([]byte(raw)); !errors.Is(err, ErrRecovery) {
			t.Errorf("%s accepted: %q", name, got)
		}
	}
}

// Only this code's own empty scope names are swept; a populated one, a
// foreign name and a scope with a child stay.
func TestSweepRemovesOnlyEmptyWorkerScopes(t *testing.T) {
	root := t.TempDir()
	scope := func(name, events string, child bool) string {
		path := filepath.Join(root, name)
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if events != "" {
			if err := os.WriteFile(filepath.Join(path, "cgroup.events"), []byte(events), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if child {
			if err := os.Mkdir(filepath.Join(path, "child"), 0700); err != nil {
				t.Fatal(err)
			}
		}
		return path
	}
	id := strings.Repeat("a", 32)
	// A plain directory with a cgroup.events file is not empty, so rmdir keeps
	// even the stale one here: the sweep only ever issues rmdir.
	empty := filepath.Join(root, scopePrefix+strings.Repeat("e", 32))
	if err := os.Mkdir(empty, 0700); err != nil {
		t.Fatal(err)
	}
	populated := scope(scopePrefix+id, "populated 1\nfrozen 0\n", false)
	foreign := scope("superbench-other-"+id, "", false)
	short := scope(scopePrefix+"abc", "", false)
	nested := scope(scopePrefix+strings.Repeat("b", 32), "populated 0\nfrozen 0\n", true)
	sweep(root)
	for _, kept := range []string{populated, foreign, short, nested} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s removed: %v", kept, err)
		}
	}
	// An empty matching directory has no cgroup.events to prove it unpopulated.
	if _, err := os.Stat(empty); err != nil {
		t.Errorf("unproved scope removed: %v", err)
	}
	if !staleScope(scopePrefix+id) || staleScope(scopePrefix+strings.ToUpper(id)) || staleScope("x"+scopePrefix+id) {
		t.Error("scope names misclassified")
	}
}
