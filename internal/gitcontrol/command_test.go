package gitcontrol

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func run(t *testing.T, dir string, control bool, args ...string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if control {
		cmd = Command(ctx, dir, args...)
	} else {
		cmd = exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = Environment(os.Environ())
		cmd.WaitDelay = 2 * time.Second
	}
	return cmd.CombinedOutput()
}
func must(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, e := run(t, dir, false, args...)
	if e != nil {
		t.Fatalf("fixture git %s: %v %s", args[0], e, out)
	}
	return strings.TrimSpace(string(out))
}
func fixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	client, server := filepath.Join(root, "client"), filepath.Join(root, "remote.git")
	must(t, root, "init", "--bare", server)
	must(t, root, "init", "-b", "main", client)
	must(t, client, "config", "user.name", "Fixture")
	must(t, client, "config", "user.email", "fixture@example.invalid")
	if e := os.WriteFile(filepath.Join(client, "tracked"), []byte("fixture"), 0600); e != nil {
		t.Fatal(e)
	}
	must(t, client, "add", "tracked")
	must(t, client, "commit", "-m", "fixture")
	must(t, client, "remote", "add", "origin", server)
	must(t, client, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	return client, server
}
func hook(t *testing.T, path, marker string, reject bool) {
	t.Helper()
	exit := "0"
	if reject {
		exit = "1"
	}
	// Marker paths come from Git configuration, avoiding shell interpolation of
	// temporary directory names. The configured path remains fixture-local.
	script := "#!/bin/sh\nmarker=$(git config fixture.marker)\nprintf ran > \"$marker\"\nexit " + exit + "\n"
	if e := os.WriteFile(path, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
}
func TestControlPushSkipsClientHooksAndTagFollowingWithoutConfigChanges(t *testing.T) {
	client, server := fixture(t)
	root := filepath.Dir(client)
	hooks := filepath.Join(root, "client-hooks")
	if e := os.Mkdir(hooks, 0700); e != nil {
		t.Fatal(e)
	}
	marker := filepath.Join(root, "client-hook-ran")
	must(t, client, "config", "fixture.marker", marker)
	hook(t, filepath.Join(hooks, "pre-push"), marker, true)
	hook(t, filepath.Join(hooks, "reference-transaction"), marker, true)
	must(t, client, "config", "core.hooksPath", hooks)
	must(t, client, "config", "push.followTags", "true")
	// Create the unrelated annotation with command-local client hooks disabled.
	if out, e := run(t, client, true, "tag", "-a", "unrelated-release", "-m", "fixture"); e != nil {
		t.Fatal(e, string(out))
	}
	before, e := os.ReadFile(filepath.Join(client, ".git", "config"))
	if e != nil {
		t.Fatal(e)
	}
	if out, e := run(t, client, true, "push", "origin", "HEAD:refs/heads/task"); e != nil {
		t.Fatal(e, string(out))
	}
	// The fetch mapping makes a successful push update the local tracking ref,
	// which would invoke reference-transaction if only pre-push were suppressed.
	if got := must(t, client, "rev-parse", "refs/remotes/origin/task"); got == "" {
		t.Fatal("no client ref transaction exercised")
	}
	if _, e = os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("client hook executed", e)
	}
	if out, e := run(t, server, true, "show-ref", "--verify", "refs/tags/unrelated-release"); e == nil {
		t.Fatal("unrelated annotated tag was followed", string(out))
	}
	after, e := os.ReadFile(filepath.Join(client, ".git", "config"))
	if e != nil || !bytes.Equal(before, after) {
		t.Fatal("host-control command changed repository configuration", e)
	}
}
func TestControlPushPreservesLocalTransportServerHooks(t *testing.T) {
	for _, kind := range []string{"pre-receive", "reference-transaction"} {
		t.Run(kind, func(t *testing.T) {
			client, server := fixture(t)
			marker := filepath.Join(filepath.Dir(client), "server-hook-ran")
			must(t, server, "config", "fixture.marker", marker)
			hook(t, filepath.Join(server, "hooks", kind), marker, true)
			if out, e := run(t, client, true, "push", "origin", "HEAD:refs/heads/rejected"); e == nil {
				t.Fatal("server hook refusal bypassed", string(out))
			}
			if _, e := os.Stat(marker); e != nil {
				t.Fatal("server hook was not exercised", e)
			}
			if out, e := run(t, server, true, "show-ref", "--verify", "refs/heads/rejected"); e == nil {
				t.Fatal("rejected server ref published", string(out))
			}
		})
	}
}
func TestControlStatusSuppressesConfiguredFilesystemMonitor(t *testing.T) {
	client, _ := fixture(t)
	marker := filepath.Join(filepath.Dir(client), "fsmonitor-ran")
	must(t, client, "config", "fixture.marker", marker)
	helper := filepath.Join(filepath.Dir(client), "fsmonitor")
	hook(t, helper, marker, true)
	must(t, client, "config", "core.fsmonitor", helper)
	before, e := os.ReadFile(filepath.Join(client, ".git", "config"))
	if e != nil {
		t.Fatal(e)
	}
	if out, e := run(t, client, true, "status", "--porcelain"); e != nil {
		t.Fatal(e, string(out))
	}
	if _, e = os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("filesystem monitor helper executed", e)
	}
	after, e := os.ReadFile(filepath.Join(client, ".git", "config"))
	if e != nil || !bytes.Equal(before, after) {
		t.Fatal("filesystem monitor config changed", e)
	}
}
func TestEnvironmentAndAmbientGitOverrides(t *testing.T) {
	env := Environment([]string{"PATH=/fixture", "BENCH_LEASE_TOKEN=private", "GIT_DIR=/foreign", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0=/foreign", "GIT_TERMINAL_PROMPT=1", "GIT_AUTHOR_NAME=ambient", "HOME=/fixture-home"})
	if strings.Join(env, "\n") != "PATH=/fixture\nHOME=/fixture-home\nGIT_TERMINAL_PROMPT=0" {
		t.Fatal("ambient overrides survived", env)
	}
	client, server := fixture(t)
	poison := filepath.Join(filepath.Dir(client), "poison-config")
	if e := os.WriteFile(poison, []byte("[core]\n bare = true\n"), 0600); e != nil {
		t.Fatal(e)
	}
	for k, v := range map[string]string{"GIT_DIR": server, "GIT_WORK_TREE": filepath.Dir(client), "GIT_INDEX_FILE": filepath.Join(filepath.Dir(client), "foreign-index"), "GIT_CONFIG_GLOBAL": poison, "GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.bare", "GIT_CONFIG_VALUE_0": "true", "GIT_NAMESPACE": "foreign", "GIT_CONFIG_PARAMETERS": "'core.bare=true'"} {
		t.Setenv(k, v)
	}
	out, e := run(t, client, true, "rev-parse", "--is-inside-work-tree")
	if e != nil || strings.TrimSpace(string(out)) != "true" {
		t.Fatal("ambient Git paths/config redirected command", string(out), e)
	}
	if _, e = os.Stat(filepath.Join(filepath.Dir(client), "foreign-index")); !os.IsNotExist(e) {
		t.Fatal("ambient index was used", e)
	}
	cmd := Command(context.Background(), client, "status")
	if cmd.WaitDelay != 2*time.Second {
		t.Fatal("unbounded inherited pipe wait")
	}
}
