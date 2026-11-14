//go:build linux || darwin

package checkpointset

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type checkpointProcessInput struct {
	State    string
	Mirrors  map[string]string
	Manifest json.RawMessage
}

// This helper is test-only. Its gate explicitly grants the disposable fixture;
// the process test makes no claim about fencing a production workspace writer.
func TestCheckpointProcessHelper(t *testing.T) {
	path := os.Getenv("SUPERBENCH_CHECKPOINT_PROCESS_INPUT")
	if path == "" {
		return
	}
	fail := func() { _, _ = os.Stdout.Write([]byte(`{"error":"checkpoint test helper failed"}`)); os.Exit(2) }
	raw, err := os.ReadFile(path)
	if err != nil {
		fail()
		return
	}
	var input checkpointProcessInput
	if json.Unmarshal(raw, &input) != nil {
		fail()
		return
	}
	manifest, err := Parse(input.Manifest)
	if err != nil {
		fail()
		return
	}
	coordinator := Coordinator{State: input.State, Mirrors: input.Mirrors, Gate: func(context.Context, Manifest) error { return nil }}
	seal, err := coordinator.Complete(context.Background(), manifest)
	if err != nil {
		fail()
		return
	}
	if json.NewEncoder(os.Stdout).Encode(seal) != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestCoordinatorProcessSIGKILLRetainsJournalAndResumesExactSeal(t *testing.T) {
	f := fixtureCoordinator(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(f.m)
	if err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(t.TempDir(), "input.json")
	input, err := json.Marshal(checkpointProcessInput{State: f.c.State, Mirrors: f.c.Mirrors, Manifest: raw})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(inputPath, input, 0600); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(t.TempDir(), "second-task-started")
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	hook := filepath.Join(f.roots["beta"], "hooks", "pre-receive")
	script := "#!/bin/sh\nwhile read old new ref; do\n if [ \"$ref\" = " + quote(f.m.Members[1].TaskRef) + " ]; then\n printf ready > " + quote(sentinel) + "\n while :; do sleep 1; done\n fi\ndone\n"
	if err = os.WriteFile(hook, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	var child *exec.Cmd
	var output, diagnostics bytes.Buffer
	var childCancel context.CancelFunc
	waited := true
	defer func() {
		if child != nil && !waited {
			_ = syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
			_ = child.Wait()
		}
		if childCancel != nil {
			childCancel()
		}
	}()
	start := func() {
		if childCancel != nil {
			childCancel()
		}
		var ctx context.Context
		ctx, childCancel = context.WithTimeout(context.Background(), 45*time.Second)
		child = exec.CommandContext(ctx, executable, "-test.run=^TestCheckpointProcessHelper$")
		child.Env = append(os.Environ(), "SUPERBENCH_CHECKPOINT_PROCESS_INPUT="+inputPath)
		child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		child.Cancel = func() error { return syscall.Kill(-child.Process.Pid, syscall.SIGKILL) }
		child.WaitDelay = 2 * time.Second
		output.Reset()
		diagnostics.Reset()
		child.Stdout = &output
		child.Stderr = &diagnostics
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		waited = false
	}
	wait := func() error { err := child.Wait(); waited = true; return err }
	start()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err = os.Stat(sentinel); err == nil {
			break
		}
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("second task publication never reached its real pre-receive hook")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Kill the helper, Git push, receive-pack, hook and its sleep as one owned
	// process group. No paused hook may publish the second ref after inspection.
	if err = syscall.Kill(-child.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if err = wait(); err == nil {
		t.Fatal("deliberate SIGKILL returned success")
	}
	first, second := f.m.Members[0], f.m.Members[1]
	for _, ref := range []string{first.TaskRef, first.PinRef} {
		if err = confirm(context.Background(), f.c.Mirrors[first.Repository], ref, first.Commit); err != nil {
			t.Fatal("first repository success was lost", err)
		}
	}
	for _, target := range []struct{ mirror, ref string }{
		{f.c.Mirrors[second.Repository], second.TaskRef},
		{f.c.Mirrors[second.Repository], second.PinRef},
	} {
		if tip, err := remoteRef(context.Background(), target.mirror, target.ref); err != nil || tip != "" {
			t.Fatal("paused second repository published", tip, err)
		}
	}
	metadata, _ := MetadataRef(f.m.BenchID)
	if tip, err := remoteRef(context.Background(), f.c.Mirrors[first.Repository], metadata); err != nil || tip != "" {
		t.Fatal("partial generation was sealed", tip, err)
	}
	journalPath := filepath.Join(f.c.State, f.m.BenchID+"-"+f.m.GenerationID+".json")
	journalBefore, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved intent
	if err = json.Unmarshal(journalBefore, &saved); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(saved.Raw, raw) || saved.Digest != Digest(raw) || saved.Prepared == "" {
		t.Fatal("durable intent did not preserve exact input")
	}
	candidate, err := sealCommit(context.Background(), f.c.Mirrors[first.Repository], f.m, saved)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := time.Parse(time.RFC3339, saved.Prepared)
	if err != nil {
		t.Fatal(err)
	}
	// Cross the timestamp boundary so deriving a new preparation time cannot
	// accidentally reproduce the journal's deterministic candidate.
	if delay := time.Until(prepared.Add(1100 * time.Millisecond)); delay > 0 {
		time.Sleep(delay)
	}
	if err = os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	start()
	if err = wait(); err != nil {
		t.Fatalf("fresh helper retry failed: %v %s %s", err, &output, &diagnostics)
	}
	var seal Seal
	if err = json.Unmarshal(output.Bytes(), &seal); err != nil {
		t.Fatal("helper did not emit a single controlled JSON result", err)
	}
	if seal.BenchID != f.m.BenchID || seal.Commit != candidate || seal.Digest != saved.Digest {
		t.Fatal("retry replaced generation or deterministic seal", seal)
	}
	journalAfter, err := os.ReadFile(journalPath)
	if err != nil || !bytes.Equal(journalBefore, journalAfter) {
		t.Fatal("retry changed prepared timestamp or exact journal bytes", err)
	}
	for _, member := range f.m.Members {
		for _, ref := range []string{member.TaskRef, member.PinRef} {
			if err = confirm(context.Background(), f.c.Mirrors[member.Repository], ref, member.Commit); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = confirm(context.Background(), f.c.Mirrors[first.Repository], metadata, candidate); err != nil {
		t.Fatal(err)
	}
	start()
	if err = wait(); err != nil {
		t.Fatal("fresh lost-reply retry failed", err)
	}
	var replay Seal
	if err = json.Unmarshal(output.Bytes(), &replay); err != nil || replay != seal {
		t.Fatal("fresh lost-reply retry changed seal", replay, err)
	}
	t.Log("real helper process group killed after first task/pin; durable Prepared/raw bytes and deterministic seal survived two fresh-process retries (fixture gate only)")
}
