package main

import (
	"bytes"
	"context"
	"encoding/json"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/localprep"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCooperativeCLIRejectsSocketAndUnrelatedFlagsBeforeStateEffects(t *testing.T) {
	for _, args := range [][]string{{"--socket=", "task"}, {"--socket=/tmp/bench.sock", "task"}, {"--digest=anything", "task"}, {"--acknowledge-stopped-writers", "task"}} {
		root := t.TempDir()
		state := filepath.Join(root, "state")
		data := filepath.Join(root, "data")
		argv := append([]string{"--state", state, "--data", data}, args...)
		if e := cooperativeCLI("prepare-local", argv, &bytes.Buffer{}, &bytes.Buffer{}); e == nil {
			t.Fatalf("accepted %v", args)
		}
		if _, e := os.Lstat(state); !os.IsNotExist(e) {
			t.Fatal("invalid local flags created state")
		}
	}
}

func TestCooperativeCLIHelpDoesNotExposeAmbientLeaseToken(t *testing.T) {
	secret := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	t.Setenv("BENCH_LEASE_TOKEN", secret)
	var out, stderr bytes.Buffer
	_ = cooperativeCLI("prepare-local", []string{"--help"}, &out, &stderr)
	if strings.Contains(out.String()+stderr.String(), secret) {
		t.Fatal("help disclosed the ambient lease capability")
	}
}

func TestCooperativeCLIUsesExplicitOwnerAndWarmTwoWriterHandoff(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	git := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, e := c.CombinedOutput(); e != nil {
			t.Fatalf("git %v: %v %s", args, e, out)
		}
	}
	git(root, "init", "--bare", remote)
	git(root, "init", "-b", "main", seed)
	if e := os.Mkdir(filepath.Join(seed, ".bench"), 0700); e != nil {
		t.Fatal(e)
	}
	for name, value := range map[string]string{".gitignore": "warm\n", ".bench/config.toml": "[setup]\nonce = 'echo once >> warm'\neach = 'echo each >> warm'\n"} {
		if e := os.WriteFile(filepath.Join(seed, name), []byte(value), 0600); e != nil {
			t.Fatal(e)
		}
	}
	git(seed, "add", ".")
	git(seed, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "fixture")
	git(seed, "remote", "add", "origin", remote)
	git(seed, "push", "origin", "main")
	s := bench.Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data")}
	b, e := s.CreateColdContext(context.Background(), "laptop", bench.Options{Repo: remote}, false)
	if e != nil {
		t.Fatal(e)
	}
	invoke := func(operation string, flags ...string) (localprep.Record, error) {
		t.Helper()
		args := append([]string{"--state", s.State, "--data", s.Data}, flags...)
		args = append(args, "laptop")
		var stdout, stderr bytes.Buffer
		e := cooperativeCLI(operation, args, &stdout, &stderr)
		var r localprep.Record
		if stdout.Len() > 0 {
			if decode := json.Unmarshal(stdout.Bytes(), &r); decode != nil {
				t.Fatal(decode)
			}
		}
		return r, e
	}
	for _, holder := range []string{"first", "second"} {
		owner, e := s.RepairLease("laptop", holder)
		if e != nil {
			t.Fatal(e)
		}
		t.Setenv("BENCH_LEASE_TOKEN", owner.Lease.Token)
		if holder == "second" {
			if _, e = invoke("prepare-local", "--token="); e == nil {
				t.Fatal("explicit empty token silently used ambient owner")
			}
			raw, e := os.ReadFile(filepath.Join(b.Path, "warm"))
			if e != nil || string(raw) != "once\neach\n" {
				t.Fatal("empty token caused preparation effects", e)
			}
		}
		observed, e := invoke("prepare-local")
		if e != nil {
			t.Fatal(e)
		}
		if observed.Mode != "cooperative" || observed.Acknowledged {
			t.Fatal("CLI fabricated a handoff")
		}
		if _, e = invoke("acknowledge-local-preparation", "--digest", observed.Digest, "--acknowledge-stopped-writers"); e != nil {
			t.Fatal(e)
		}
		if _, e = s.Checkpoint("laptop", owner.Lease.Token); e != nil {
			t.Fatal(e)
		}
		if e = s.Release("laptop", owner.Lease.Token); e != nil {
			t.Fatal(e)
		}
	}
	raw, e := os.ReadFile(filepath.Join(b.Path, "warm"))
	if e != nil || string(raw) != "once\neach\neach\n" {
		t.Fatalf("CLI lost warm handoff: %q %v", raw, e)
	}
}
