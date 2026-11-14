package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBoundedLogDrainsOutputAndMarksTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	capture, e := openLog(path, 1024)
	if e != nil {
		t.Fatal(e)
	}
	defer capture.close()
	cmd := exec.Command("sh", "-c", "head -c 131072 /dev/zero")
	cmd.Stdout = capture.writer
	cmd.Stderr = capture.writer
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	capture.writer.Close()
	if e = cmd.Wait(); e != nil {
		t.Fatal(e)
	}
	if e = capture.finish(); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if len(raw) != 1024 || !bytes.HasSuffix(raw, []byte(logMarker)) || !capture.truncated {
		t.Fatal("output not bounded/marked", len(raw))
	}
}
func TestRunReportsTruncationWithoutLosingCheckpoint(t *testing.T) {
	s, r := fixture(t)
	if _, e := s.Create("task", Options{Repo: r}, false); e != nil {
		t.Fatal(e)
	}
	s.executor = &testExecutor{}
	result, e := s.Run(context.Background(), "task", "writer", []string{"sh", "-c", "head -c 2097152 /dev/zero"}, true)
	if e != nil {
		t.Fatal(e)
	}
	if !result.LogTruncated || result.Bench.Checkpoint == "" || result.Bench.Lease != nil {
		t.Fatal("truncated command lost lifecycle outcome")
	}
}
func TestLogAdmissionReservesOutstandingCapacity(t *testing.T) {
	s, r := fixture(t)
	if _, e := s.Create("task", Options{Repo: r}, false); e != nil {
		t.Fatal(e)
	}
	c := HostConfig{DataRoot: s.Data, QuotaHelper: "/test/helper", MinDataFreeBytes: 1024, MinStateFreeBytes: 1024, MaxLogBytes: 1 << 20, MaxLogsBytes: 1 << 20, MaxLogFiles: 5}
	// Existing held execution reserves the full per-run cap even before its log exists.
	if e := s.transaction(func(db database) error {
		db["task"].Lease = &Lease{Holder: "writer", Token: "fixture", Execution: &Execution{Log: filepath.Join(s.State, "run-pending.log"), LogLimitBytes: 1 << 20, Phase: "starting"}}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	// Record a managed config directly: this test exercises admission, not the root helper.
	fakeSpace(&s)
	raw, e := json.Marshal(c)
	if e != nil {
		t.Fatal(e)
	}
	if e = atomicWrite(s.State, filepath.Join(s.State, "host.json"), raw); e != nil {
		t.Fatal(e)
	}
	if e := s.transaction(func(db database) error { _, e := s.reserveLog(db); return e }); e == nil || !strings.Contains(e.Error(), "log budget") {
		t.Fatal("outstanding log reservation ignored", e)
	}
}
