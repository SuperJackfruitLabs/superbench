package reap

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A FIFO swapped in between the Lstat and the open must not block the walk: the
// open is nonblocking and the identity check then refuses the changed entry.
func TestSnapshotDoesNotBlockOnAFIFOSwappedInAfterLstat(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file")
	put(t, path, "regular")
	swapped := false
	beforeOpen = func(relative string) {
		if relative != "file" || swapped {
			return
		}
		swapped = true
		if err := os.Remove(path); err != nil {
			t.Error(err)
		}
		if err := syscall.Mkfifo(path, 0600); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { beforeOpen = nil })
	done := make(chan error, 1)
	go func() { _, err := Snapshot(root, nil); done <- err }()
	select {
	case err := <-done:
		if !swapped || err == nil || !strings.Contains(err.Error(), "file changed during snapshot") {
			t.Fatalf("swapped FIFO not refused: swapped=%v %v", swapped, err)
		}
	case <-time.After(5 * time.Second):
		// Release the blocked reader so the test binary can exit.
		if w, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			w.Close()
		}
		<-done
		t.Fatal("snapshot blocked opening a FIFO swapped in after its Lstat")
	}
}
