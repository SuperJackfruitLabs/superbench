package bench

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

func TestLeaseHelper(t *testing.T) {
	if os.Getenv("SUPERBENCH_TEST_HELPER") != "1" {
		return
	}
	s := Store{State: os.Getenv("SUPERBENCH_TEST_STATE"), Data: os.Getenv("SUPERBENCH_TEST_DATA")}
	if _, err := s.Lease("task", "process"); err != nil {
		os.Exit(23)
	}
	os.Exit(0)
}
func TestLeaseAcrossProcesses(t *testing.T) {
	s, r := fixture(t)
	if _, err := s.Create("task", Options{Repo: r}, false); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			cmd := exec.Command(executable, "-test.run=^TestLeaseHelper$")
			cmd.Env = append(os.Environ(), "SUPERBENCH_TEST_HELPER=1", "SUPERBENCH_TEST_STATE="+s.State, "SUPERBENCH_TEST_DATA="+s.Data)
			results <- cmd.Run()
		})
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
			continue
		}
		exit, ok := err.(*exec.ExitError)
		if !ok || exit.ExitCode() != 23 {
			t.Fatalf("unexpected child failure: %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("expected one process lease winner, got %d", wins)
	}
}
func TestCorruptStateFailsClosed(t *testing.T) {
	s, _ := fixture(t)
	if err := os.MkdirAll(s.State, 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(s.State, "benches.json"), "{broken")
	if _, err := s.Status(""); err == nil {
		t.Fatal("corrupt state accepted")
	}
	raw, err := os.ReadFile(filepath.Join(s.State, "benches.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "{broken" {
		t.Fatal("corrupt state overwritten")
	}
}
