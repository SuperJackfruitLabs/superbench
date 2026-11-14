package gitcontrol

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
)

func canonicalTemp(t *testing.T) string {
	t.Helper()
	p, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func fixtureIntent(t *testing.T, state, root string) (string, controlIntent) {
	t.Helper()
	dir, e := controlDir(state, true)
	if e != nil {
		t.Fatal(e)
	}
	j := controlIntent{Version: controlVersion, Root: root, ID: strings.Repeat("a", 32), HostID: strings.Repeat("b", 64), BootID: "fixture-boot"}
	if e = saveControl(dir, j); e != nil {
		t.Fatal(e)
	}
	return filepath.Join(dir, j.ID+".json"), j
}
func TestRunContextSelectionAndCanceledPreflight(t *testing.T) {
	ctx := context.Background()
	if e := Run(ctx, exec.Command("true")); e != nil {
		t.Fatal(e)
	}
	if e := Run(WithRunner(ctx, nil), exec.Command("true")); e != nil {
		t.Fatal(e)
	}
	state := canonicalTemp(t)
	marker := filepath.Join(state, "must-not-start")
	r := &Runner{State: state, Root: state}
	if e := Run(WithRunner(ctx, r), exec.Command("touch", marker)); e == nil {
		t.Fatal("ordinary filesystem accepted as execution root")
	}
	if _, e := os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("command started before root validation", e)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if e := r.Run(canceled, exec.Command("touch", marker)); e != context.Canceled {
		t.Fatal("canceled preflight changed", e)
	}
	if Available(state) {
		t.Fatal("regular filesystem advertised managed containment")
	}
	if e := r.Run(ctx, nil); e == nil {
		t.Fatal("nil command accepted")
	}
}
func TestRecoveryNoIntentAndUnrelatedFiles(t *testing.T) {
	state := canonicalTemp(t)
	if e := Recover(filepath.Join(state, "absent")); e != nil {
		t.Fatal(e)
	}
	if e := Recover(state); e != nil {
		t.Fatal(e)
	}
	dir, e := controlDir(state, true)
	if e != nil {
		t.Fatal(e)
	}
	unrelated := filepath.Join(dir, "unrelated.json")
	if e = os.WriteFile(unrelated, []byte("not an intent"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = Recover(state); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(unrelated); e != nil {
		t.Fatal("recovery removed unrelated data", e)
	}
	if e = Recover("relative-state"); e == nil {
		t.Fatal("relative recovery state accepted")
	}
}
func TestRecoveryNeverWritesOrdinaryFilesystemKillFile(t *testing.T) {
	state, root := canonicalTemp(t), canonicalTemp(t)
	kill := filepath.Join(root, "cgroup.kill")
	if e := os.WriteFile(kill, []byte("must remain unchanged"), 0600); e != nil {
		t.Fatal(e)
	}
	path, _ := fixtureIntent(t, state, root)
	if e := Recover(state); e == nil {
		t.Fatal("ordinary filesystem recovery authority accepted")
	}
	raw, e := os.ReadFile(kill)
	if e != nil || string(raw) != "must remain unchanged" {
		t.Fatal("recovery wrote fake cgroup.kill", e)
	}
	if _, e = os.Stat(path); e != nil {
		t.Fatal("unsafe intent was discarded", e)
	}
}
func TestRecoveryRefusesCorruptedOrPublicIntents(t *testing.T) {
	for _, kind := range []string{"unknown field", "duplicate field", "case alias", "identity mismatch", "public mode", "oversize", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			state := canonicalTemp(t)
			path, j := fixtureIntent(t, state, canonicalTemp(t))
			raw, _ := json.Marshal(j)
			switch kind {
			case "unknown field":
				raw = append(raw[:len(raw)-1], []byte(",\"Extra\":\"value\"}")...)
			case "duplicate field":
				raw = append(raw[:len(raw)-1], []byte(",\"Root\":\"/foreign\"}")...)
			case "case alias":
				raw = append(raw[:len(raw)-1], []byte(",\"root\":\"/foreign\"}")...)
			case "identity mismatch":
				j.ID = strings.Repeat("c", 32)
				raw, _ = json.Marshal(j)
			case "oversize":
				raw = []byte(strings.Repeat("x", maxIntent+1))
			case "symlink":
				if e := os.Remove(path); e != nil {
					t.Fatal(e)
				}
				other := filepath.Join(state, "other")
				if e := os.WriteFile(other, raw, 0600); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(other, path); e != nil {
					t.Fatal(e)
				}
			}
			if kind != "symlink" {
				if e := os.WriteFile(path, raw, 0600); e != nil {
					t.Fatal(e)
				}
			}
			if kind == "public mode" {
				if e := os.Chmod(path, 0644); e != nil {
					t.Fatal(e)
				}
			}
			if e := Recover(state); e == nil {
				t.Fatal("unsafe intent accepted", kind)
			}
			if _, e := os.Lstat(path); e != nil {
				t.Fatal("invalid intent removed", e)
			}
		})
	}
}
func TestRecoveryBoundsIntentCount(t *testing.T) {
	state := canonicalTemp(t)
	dir, e := controlDir(state, true)
	if e != nil {
		t.Fatal(e)
	}
	j := controlIntent{Version: controlVersion, Root: state, HostID: strings.Repeat("b", 64), BootID: "fixture-boot"}
	for i := 0; i < 1001; i++ {
		j.ID = fmt.Sprintf("%032x", i)
		raw, _ := json.Marshal(j)
		if e = os.WriteFile(filepath.Join(dir, j.ID+".json"), raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	if e = Recover(state); e == nil || !strings.Contains(e.Error(), "intent limit") {
		t.Fatal("unbounded recovery intent scan", e)
	}
}
func TestRecoveryRefusesForeignHostBeforeFencing(t *testing.T) {
	if !execution.Supported || validateRoot("/sys/fs/cgroup") != nil {
		t.Skip("Linux cgroup filesystem not available")
	}
	state := canonicalTemp(t)
	path, _ := fixtureIntent(t, state, "/sys/fs/cgroup")
	if e := Recover(state); e == nil {
		t.Fatal("foreign-host intent accepted")
	}
	if _, e := os.Stat(path); e != nil {
		t.Fatal("foreign-host authority was removed", e)
	}
}
func TestControlStateRejectsSymlinkAndPublicJournalDirectory(t *testing.T) {
	state := canonicalTemp(t)
	parent := canonicalTemp(t)
	link := filepath.Join(parent, "state")
	if e := os.Symlink(state, link); e != nil {
		t.Fatal(e)
	}
	if _, e := controlDir(link, true); e == nil {
		t.Fatal("symlink state accepted")
	}
	dir, e := controlDir(state, true)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(dir, 0755); e != nil {
		t.Fatal(e)
	}
	if e = Recover(state); e == nil {
		t.Fatal("nonprivate journal directory accepted")
	}
}

func TestControlTimeoutSelectionPreservesParentDeadline(t *testing.T) {
	for _, limit := range []time.Duration{0, 5 * time.Second, time.Hour} {
		ctx, cancel, e := (Runner{Timeout: limit}).deadline(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		deadline, ok := ctx.Deadline()
		expected := limit
		if expected == 0 {
			expected = 2 * time.Minute
		}
		remaining := time.Until(deadline)
		if !ok || remaining > expected || remaining < expected-time.Second {
			t.Fatal("selected timeout changed", limit, remaining)
		}
		cancel()
	}
	parent, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	child, cancel, e := (Runner{Timeout: time.Hour}).deadline(parent)
	if e != nil {
		t.Fatal(e)
	}
	defer cancel()
	want, _ := parent.Deadline()
	got, _ := child.Deadline()
	if !got.Equal(want) {
		t.Fatal("control timeout extended parent deadline")
	}
	stop()
	if child.Err() != context.Canceled {
		t.Fatal("parent cancellation was lost", child.Err())
	}
}
func TestInvalidControlTimeoutRefusesBeforeFilesystemEffects(t *testing.T) {
	for _, limit := range []time.Duration{-time.Nanosecond, time.Hour + time.Nanosecond} {
		root := canonicalTemp(t)
		state := filepath.Join(root, "must-not-create")
		r := Runner{State: state, Root: state, Timeout: limit}
		if e := r.Run(context.Background(), exec.Command("true")); e == nil || !strings.Contains(e.Error(), "timeout") {
			t.Fatal("invalid timeout accepted or inspected roots first", e)
		}
		if _, e := os.Lstat(state); !os.IsNotExist(e) {
			t.Fatal("invalid timeout created state", e)
		}
	}
}
