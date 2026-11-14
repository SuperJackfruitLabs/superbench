package bench

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

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
