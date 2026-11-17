//go:build linux && freshstoragefixture

package mountnsfixture

import (
	"runtime"
	"syscall"
	"testing"
)

// The private namespace step must never run on the thread-group leader, even
// when the caller itself holds the leader. Unprivileged: no unshare happens.
func TestWorkerThreadIsNeverTheLeader(t *testing.T) {
	check := func() {
		for n := 0; n < 200; n++ {
			var tid int
			if err := onWorkerThread(func() error { tid = syscall.Gettid(); return nil }); err != nil {
				t.Fatal(err)
			}
			if tid == syscall.Getpid() {
				t.Fatal("the namespace step ran on the thread-group leader")
			}
		}
	}
	check()
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		check()
	}()
	<-done
}

func TestMountIDReadsThisThreadsMountinfo(t *testing.T) {
	if id, err := MountID("/"); err != nil || id == "" {
		t.Fatalf("root mount ID: %q %v", id, err)
	}
}
