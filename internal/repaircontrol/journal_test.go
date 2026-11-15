package repaircontrol

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

func fixture(t *testing.T) (*Journal, Admission) {
	t.Helper()
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := Create(state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	a := Admission{Instance: j.Instance(), ID: strings.Repeat("1", 32), BenchID: strings.Repeat("2", 32), RepairID: "reviewed-repair", Approval: strings.Repeat("3", 64), OwnerDigest: strings.Repeat("4", 64), Intent: operationcontrol.Intent{Target: operationcontrol.Target{UID: uint32(os.Geteuid()), RequestID: "repair-internal", Digest: strings.Repeat("5", 64)}, Key: "card", Operation: "setup", Epoch: strings.Repeat("6", 32)}, Control: operationcontrol.Control{State: state, Root: "/fixture/cgroups", HostID: strings.Repeat("7", 64), BootID: "fixture-boot"}}
	return j, a
}

func TestPreparedAdmissionHasNoEffectsAndOneControllerConsumesAcrossHandles(t *testing.T) {
	j, a := fixture(t)
	ctx := context.Background()
	if err := j.Reserve(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Scope(ctx, a, strings.Repeat("8", 32)); err == nil {
		t.Fatal("prepared row admitted effects")
	}
	other, err := OpenExisting(a.Control.State, a.Instance)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for n, handle := range []*Journal{j, other} {
		wg.Add(1)
		go func(n int, h *Journal) {
			defer wg.Done()
			results <- h.Start(ctx, a, strings.Repeat(string(rune('8'+n)), 32))
		}(n, handle)
	}
	wg.Wait()
	close(results)
	won, lost := 0, 0
	for err := range results {
		if err == nil {
			won++
		} else if errors.Is(err, ErrRunning) {
			lost++
		} else {
			t.Fatal(err)
		}
	}
	if won != 1 || lost != 1 {
		t.Fatal("admission consumed more than once", won, lost)
	}
	r, err := j.Get(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := j.Scope(ctx, a, r.Controller)
	if err != nil {
		t.Fatal(err)
	}
	called := 0
	if err := operationcontrol.Effect(scoped, func() error { called++; return nil }); err != nil || called != 1 {
		t.Fatal(called, err)
	}
	if err := other.RequestCancel(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := operationcontrol.Effect(scoped, func() error { called++; return nil }); !errors.Is(err, ErrCancelled) || called != 1 {
		t.Fatal("late effect after durable cancellation", called, err)
	}
	if err := j.Start(ctx, a, strings.Repeat("a", 32)); err == nil {
		t.Fatal("cancelled controller restarted")
	}
}

func TestRestartNeverReconstructsMissingOrReplacedAdmissionAndCompletedClosesEffects(t *testing.T) {
	j, a := fixture(t)
	ctx := context.Background()
	controller := strings.Repeat("8", 32)
	if err := j.Reserve(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := j.Start(ctx, a, controller); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	resumed, err := OpenExisting(a.Control.State, a.Instance)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if err := resumed.Start(ctx, a, strings.Repeat("9", 32)); !errors.Is(err, ErrRunning) {
		t.Fatal("crash replay admitted", err)
	}
	if err := resumed.Complete(ctx, a, controller); err != nil {
		t.Fatal(err)
	}
	if _, err := resumed.Scope(ctx, a, controller); err == nil {
		t.Fatal("completed controller admitted effects")
	}
	if err := resumed.Reserve(ctx, a); err == nil {
		t.Fatal("completed admission recycled")
	}
	if err := resumed.RequestCancel(ctx, a); err == nil {
		t.Fatal("completed cutoff changed to cancellation")
	}
	if err := os.Rename(filepath.Join(a.Control.State, databaseName), filepath.Join(a.Control.State, "preserved.db")); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenExisting(a.Control.State, a.Instance); err == nil {
		t.Fatal("missing journal reconstructed")
	}
	if err := resumed.Barrier(ctx, a); err == nil {
		t.Fatal("missing named database accepted through open handle")
	}
	if err := os.WriteFile(filepath.Join(a.Control.State, databaseName), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenExisting(a.Control.State, a.Instance); err == nil {
		t.Fatal("replacement database adopted")
	}
}

func TestCancelAdmissionWaitsOnlyForExactShortEffectAndPersistsAcrossReopen(t *testing.T) {
	j, a := fixture(t)
	ctx := context.Background()
	controller := strings.Repeat("8", 32)
	if err := j.Reserve(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := j.Start(ctx, a, controller); err != nil {
		t.Fatal(err)
	}
	scoped, err := j.Scope(ctx, a, controller)
	if err != nil {
		t.Fatal(err)
	}
	other, err := OpenExisting(a.Control.State, a.Instance)
	if err != nil {
		t.Fatal(err)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- operationcontrol.Effect(scoped, func() error { close(entered); <-release; return nil })
	}()
	<-entered
	short, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
	defer cancel()
	if err := other.RequestCancel(short, a); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("cancel crossed in-flight short effect", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := other.RequestCancel(ctx, a); err != nil {
		t.Fatal(err)
	}
	other.Close()
	other, err = OpenExisting(a.Control.State, a.Instance)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := other.Barrier(ctx, a); err != nil {
		t.Fatal("cancel barrier lost across restart", err)
	}
	if err := other.ConfirmCancelled(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := other.Barrier(ctx, a); err != nil {
		t.Fatal(err)
	}
}
