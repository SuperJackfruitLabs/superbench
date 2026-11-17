package freshliveness

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func liveStub() (*Liveness, map[string]string, map[string][]int, map[int][2]bool) {
	events := map[string]string{"a": "populated 0\nfrozen 0\n", "b": "populated 1\n"}
	members := map[string][]int{"b": {10, 11}}
	// pid: {payload account, uses the bench}
	procs := map[int][2]bool{10: {true, false}, 11: {false, false}, 12: {false, true}, 13: {true, false}}
	l := &Liveness{
		Scopes:  func() ([]string, error) { return []string{"a", "b"}, nil },
		Events:  func(s string) ([]byte, error) { return []byte(events[s]), nil },
		Members: func(s string) ([]int, error) { return members[s], nil },
		Processes: func() ([]int, error) {
			var pids []int
			for pid := range procs {
				pids = append(pids, pid)
			}
			return pids, nil
		},
		Payload: func(pid int) (bool, error) { return procs[pid][0], nil },
		Uses:    func(pid int) (bool, error) { return procs[pid][1], nil },
	}
	return l, events, members, procs
}

func TestLivenessReadsScopesAndPayloadProcesses(t *testing.T) {
	l, events, members, procs := liveStub()
	if err := l.Idle(); err != nil {
		t.Fatalf("idle bench refused: %v", err)
	}
	// A populated scope holding a process in the bench, whatever its account.
	procs[11] = [2]bool{false, true}
	if err := l.Idle(); !errors.Is(err, ErrBusy) {
		t.Fatalf("a process in a populated scope: %v", err)
	}
	// The same process in an unpopulated scope is not the scope's; as a
	// non-payload process outside any scope it is not seen either.
	events["b"] = "populated 0\n"
	if err := l.Idle(); err != nil {
		t.Fatalf("unpopulated scope: %v", err)
	}
	// A payload process anywhere that uses the bench.
	procs[13] = [2]bool{true, true}
	if err := l.Idle(); !errors.Is(err, ErrBusy) {
		t.Fatalf("a payload process outside the scopes: %v", err)
	}
	procs[13] = [2]bool{true, false}
	// A malformed events file reads as populated.
	events["a"] = "populated\n"
	members["a"] = []int{11}
	if err := l.Idle(); !errors.Is(err, ErrBusy) {
		t.Fatalf("malformed cgroup.events: %v", err)
	}
	for raw, want := range map[string]bool{"populated 0\n": true, " populated   0 \nfrozen 1\n": true, "populated 1\n": false, "populated 0\npopulated 0\n": false, "": false, "populated 0 1\n": false} {
		if Unpopulated([]byte(raw)) != want {
			t.Errorf("Unpopulated(%q) != %v", raw, want)
		}
	}
}

// A stalled worker is killed and the read refuses as payload activity;
// progress bytes keep it alive and never reach the reply.
func TestWatchedReadKillsAStalledWorker(t *testing.T) {
	r, w := io.Pipe()
	killed := make(chan struct{})
	kill := func() error { close(killed); return r.CloseWithError(io.ErrClosedPipe) }
	go func() {
		for range 5 {
			w.Write([]byte("."))
			time.Sleep(20 * time.Millisecond)
		}
	}()
	start := time.Now()
	if _, err := WatchedRead(r, 100*time.Millisecond, kill, 1024); !errors.Is(err, ErrPayloadActive) {
		t.Fatalf("stalled worker: %v", err)
	}
	<-killed
	if time.Since(start) < 150*time.Millisecond {
		t.Fatal("progress did not keep the worker alive")
	}
	r, w = io.Pipe()
	go func() {
		w.Write([]byte("..."))
		w.Write([]byte(".{\"Version\":1}"))
		w.Close()
	}()
	out, err := WatchedRead(r, time.Second, func() error { t.Error("a working worker killed"); return nil }, 1024)
	if err != nil || string(out) != `{"Version":1}` {
		t.Fatalf("reply: %q %v", out, err)
	}
	r, w = io.Pipe()
	go func() { w.Write([]byte("{" + strings.Repeat("x", 2000))); w.Close() }()
	if _, err = WatchedRead(r, time.Second, func() error { return nil }, 1024); !errors.Is(err, ErrOversized) {
		t.Fatalf("oversized reply: %v", err)
	}
}

// A worker that never writes its ready line is killed after idle.
func TestWatchedReadFullKillsAWorkerThatNeverGetsReady(t *testing.T) {
	r, w := io.Pipe()
	defer w.Close()
	kill := func() error { return r.CloseWithError(io.ErrClosedPipe) }
	buf := make([]byte, 4)
	if err := WatchedReadFull(r, buf, 50*time.Millisecond, kill); !errors.Is(err, ErrPayloadActive) {
		t.Fatalf("stalled ready: %v", err)
	}
	r, w = io.Pipe()
	go func() { w.Write([]byte("rea")); w.Write([]byte("dy")); w.Close() }()
	if err := WatchedReadFull(r, buf, time.Second, func() error { t.Error("a ready worker killed"); return nil }); err != nil || string(buf) != "read" {
		t.Fatalf("ready: %q %v", buf, err)
	}
}
