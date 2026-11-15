package localprep_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/localprep"
)

func TestCooperativeControllerCrashHelper(t *testing.T) {
	if os.Getenv("SUPERBENCH_LOCAL_CRASH_HELPER") != "1" {
		return
	}
	args := os.Args
	index := -1
	for i, a := range args {
		if a == "--" {
			index = i
			break
		}
	}
	if index < 0 || len(args) != index+4 {
		os.Exit(91)
	}
	tokenBytes, e := os.ReadFile(args[index+3])
	if e != nil {
		os.Exit(92)
	}
	m := localprep.Manager{State: args[index+1], Data: args[index+2]}
	_, _ = m.Prepare(context.Background(), "laptop", string(tokenBytes), false, false)
	os.Exit(93)
}

func TestCooperativeControllerCrashRetainsUnknownIntentAndLease(t *testing.T) {
	s, b := fixture(t)
	config := "[setup]\nonce = 'echo $$ > leader.pid; sleep 30 & echo $! > child.pid; wait'\n"
	if e := os.WriteFile(filepath.Join(b.Path, ".bench", "config.toml"), []byte(config), 0600); e != nil {
		t.Fatal(e)
	}
	owner, e := s.RepairLease("laptop", "crashed-writer")
	if e != nil {
		t.Fatal(e)
	}
	tokenFile := filepath.Join(t.TempDir(), "owner")
	if e = os.WriteFile(tokenFile, []byte(owner.Lease.Token), 0600); e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCooperativeControllerCrashHelper$", "--", s.State, s.Data, tokenFile)
	cmd.Env = append(os.Environ(), "SUPERBENCH_LOCAL_CRASH_HELPER=1")
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	deadline := time.Now().Add(5 * time.Second)
	var pids []int
	for time.Now().Before(deadline) {
		leader, le := os.ReadFile(filepath.Join(b.Path, "leader.pid"))
		child, ce := os.ReadFile(filepath.Join(b.Path, "child.pid"))
		if le == nil && ce == nil {
			a, _ := strconv.Atoi(strings.TrimSpace(string(leader)))
			b, _ := strconv.Atoi(strings.TrimSpace(string(child)))
			if a > 0 && b > 0 {
				pids = []int{a, b}
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(pids) != 2 {
		t.Fatal("owned hook did not start")
	}
	t.Cleanup(func() {
		for _, id := range pids {
			p, _ := os.FindProcess(id)
			_ = p.Kill()
		}
	})
	if e = cmd.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	_ = cmd.Wait()
	m := localprep.Manager{State: s.State, Data: s.Data}
	record, e := m.Status(context.Background(), "laptop", owner.Lease.Token)
	if e != nil {
		t.Fatal(e)
	}
	if record.Observed || record.Acknowledged || record.Successful || record.ExitCode != nil || !record.UncertainOnce {
		t.Fatalf("crash fabricated completion %#v", record)
	}
	if _, e = m.Prepare(context.Background(), "laptop", owner.Lease.Token, false, false); e == nil {
		t.Fatal("restart automatically reran interrupted hook")
	}
	if e = s.Release("laptop", owner.Lease.Token); e == nil {
		t.Fatal("crash lost held ownership")
	}
	// Independent cleanup is required before the test can assert cooperative stop.
	for _, id := range pids {
		p, _ := os.FindProcess(id)
		_ = p.Kill()
	}
	ack, e := m.Acknowledge(context.Background(), "laptop", owner.Lease.Token, record.Digest, true)
	if e != nil {
		t.Fatal(e)
	}
	if ack.Successful || !ack.UncertainOnce {
		t.Fatal("stopped unknown hook became success")
	}
}
