package payloadbootstraptest

// The member Git steps, run unprivileged outside the package, which keeps the
// package within its size bound.

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	. "forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
)

// fixtureGit runs Git as the test's own account, outside any bootstrap policy,
// to build an upstream and a daemon-style mirror.
func fixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(GitEnvironment(), "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	cmd.Env[0] = "PATH=" + os.Getenv("PATH")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// runStep runs one fixed step with the argv and environment the manager uses, without the
// daemon credential and control scope that need a privileged Linux host.
func runStep(step GitStep) (string, error) {
	cmd := exec.Command("git", append(GitConfig(), step.Args...)...)
	cmd.Dir = step.Dir
	cmd.Env = GitEnvironment()
	cmd.Env[0] = "PATH=" + os.Getenv("PATH")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// movedMirror is a mirror laid out as the daemon's: a bare repository whose
// local branch is main and whose refresh keeps the remote's branches under
// refs/remotes/origin/*. The card branch moved two commits past main, so
// neither of its commits is reachable from any local branch of the mirror.
func movedMirror(t *testing.T) (mirror, main, ancestor, tip string) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	upstream := filepath.Join(base, "upstream")
	fixtureGit(t, base, "init", "--initial-branch=main", upstream)
	commit := func(name string) string {
		if err := os.WriteFile(filepath.Join(upstream, name), []byte(name+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		fixtureGit(t, upstream, "add", name)
		fixtureGit(t, upstream, "commit", "-m", name)
		return fixtureGit(t, upstream, "rev-parse", "HEAD")
	}
	main = commit("main.txt")
	fixtureGit(t, upstream, "checkout", "-b", "card/moved")
	ancestor = commit("first.txt")
	tip = commit("second.txt")
	fixtureGit(t, upstream, "checkout", "main")
	mirror = filepath.Join(base, "mirror.git")
	fixtureGit(t, base, "init", "--bare", "--initial-branch=main", mirror)
	fixtureGit(t, mirror, "config", "remote.origin.url", upstream)
	fixtureGit(t, mirror, "fetch", "--no-tags", "origin", "+refs/heads/*:refs/remotes/origin/*")
	fixtureGit(t, mirror, "update-ref", "refs/heads/main", main)
	if heads := fixtureGit(t, mirror, "for-each-ref", "--contains", ancestor, "--format=%(refname)"); heads != "refs/remotes/origin/card/moved" {
		t.Fatalf("the source must be reachable only from a remote-tracking ref: %q", heads)
	}
	return mirror, main, ancestor, tip
}

// A source commit the mirror holds only under refs/remotes/origin/* is checked
// out exactly, whether it is that branch's tip or behind it.
func TestMemberStepsCheckOutASourceOnlyUnderRemoteTrackingRefs(t *testing.T) {
	mirror, main, ancestor, tip := movedMirror(t)
	refsBefore := fixtureGit(t, mirror, "for-each-ref")
	for name, source := range map[string]string{"tip": tip, "behind-tip": ancestor} {
		t.Run(name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			checkout := filepath.Join(root, "api")
			admin := filepath.Join(root, ".superbench-git", "api")
			if err = os.Mkdir(filepath.Dir(admin), 0700); err != nil { // as Begin does
				t.Fatal(err)
			}
			member := Member{Name: "api", Repository: "https://example.invalid/api.git", Branch: "card/moved", Source: source, ObjectFormat: "sha1"}
			for _, step := range MemberSteps(root, mirror, checkout, admin, member) {
				if out, err := runStep(step); err != nil {
					t.Fatalf("git %v: %v %s", step.Args, err, out)
				}
			}
			for args, want := range map[string]string{
				"symbolic-ref --quiet HEAD":                         "refs/heads/card/moved",
				"rev-parse --verify HEAD^{commit}":                  source,
				"config --get-all remote.origin.url":                member.Repository,
				"for-each-ref --format=%(refname)":                  "refs/heads/card/moved\nrefs/heads/main\nrefs/remotes/origin/HEAD\nrefs/remotes/origin/main",
				"rev-parse --verify refs/remotes/origin/main":       main,
				"status --porcelain=v1 --untracked-files=all":       "",
				"rev-parse --path-format=absolute --git-common-dir": admin,
			} {
				if got := fixtureGit(t, checkout, strings.Fields(args)...); got != want {
					t.Fatalf("git %s = %q, want %q", args, got, want)
				}
			}
			fixtureGit(t, checkout, "fsck", "--full", "--no-reflogs")
			if _, err := os.Lstat(filepath.Join(admin, "FETCH_HEAD")); !os.IsNotExist(err) {
				t.Fatalf("the source fetch wrote FETCH_HEAD: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(checkout, "first.txt")); err != nil {
				t.Fatalf("the source tree is not checked out: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(checkout, "second.txt")); (source == tip) != (err == nil) {
				t.Fatalf("checkout is not exactly the source: %v", err)
			}
		})
	}
	if refsAfter := fixtureGit(t, mirror, "for-each-ref"); refsAfter != refsBefore {
		t.Fatalf("materializing changed the mirror's refs:\n%s\nwas\n%s", refsAfter, refsBefore)
	}
}

// The steps are a fixed allow-list: the only variable words are the daemon's
// own paths and the spec's validated branch, source and registered URL.
func TestMemberStepsAreExact(t *testing.T) {
	member := Member{Name: "api", Repository: "https://example.invalid/api.git", Branch: "card/x", Source: strings.Repeat("a", 40), ObjectFormat: "sha1"}
	got := MemberSteps("/r", "/m.git", "/r/api", "/r/.superbench-git/api", member)
	want := []GitStep{
		{Dir: "/r", Args: []string{"clone", "--template=", "--no-local", "--no-hardlinks", "--no-checkout", "--separate-git-dir", "/r/.superbench-git/api", "--", "/m.git", "/r/api"}},
		{Dir: "/r/api", Args: []string{"-c", "protocol.version=2", "fetch", "--no-tags", "--no-write-fetch-head", "--no-auto-maintenance", "--recurse-submodules=no", "--refmap=", "--", "/m.git", member.Source}},
		{Dir: "/r/api", Args: []string{"config", "--local", "remote.origin.url", member.Repository}},
		{Dir: "/r/api", Args: []string{"checkout", "--no-guess", "-b", "card/x", member.Source, "--"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("member steps changed:\n%q\nwant\n%q", got, want)
	}
	wantConfig := []string{"--no-optional-locks", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "credential.helper=", "-c", "protocol.ext.allow=never", "-c", "protocol.file.allow=always"}
	wantEnv := []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_ATTR_NOSYSTEM=1", "GIT_TEMPLATE_DIR=/dev/null"}
	if !reflect.DeepEqual(GitConfig(), wantConfig) || !reflect.DeepEqual(GitEnvironment(), wantEnv) {
		t.Fatalf("bootstrap Git config or environment changed: %q %q", GitConfig(), GitEnvironment())
	}
}
