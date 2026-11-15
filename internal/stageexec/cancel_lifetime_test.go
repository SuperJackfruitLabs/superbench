package stageexec

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
)

func TestRunDoesNotOutliveItsCancellationMonitor(t *testing.T) {
	p := plan(t, "while [ ! -f leave ]; do sleep 0.01; done")
	r := defaults(p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fencing, release, reconciled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	r.Start = func(_ context.Context, c *exec.Cmd, _ *execution.Execution) error {
		err := c.Start()
		if err == nil {
			cancel()
		}
		return err
	}
	r.Backend = fenceFunc(func(*execution.Execution) error { close(fencing); <-release; return nil })
	r.Cancel = func() { close(reconciled) }
	done := make(chan error, 1)
	go func() { _, err := r.Run(ctx, p); done <- err }()
	select {
	case <-fencing:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation monitor did not enter fence")
	}
	if err := os.WriteFile(filepath.Join(p.Dir, "leave"), []byte("exit"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		t.Fatalf("Run returned while cancellation callback could still stop a later same-token operation: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	unblock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not finish after its monitor")
	}
	select {
	case <-reconciled:
	default:
		t.Fatal("runner returned before reconciliation callback ended")
	}
}
