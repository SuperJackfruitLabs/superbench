package service

import (
	"database/sql/driver"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

func TestManagedGateRejectsWrongInvocationAndRequiresExactRelease(t *testing.T) {
	trial := t.TempDir()
	dir := filepath.Join(trial, "cancellation-gate")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "observed.json")
	if err := os.WriteFile(marker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	target := operationcontrol.Target{UID: 1001, RequestID: "materialization-create", Digest: strings.Repeat("a", 64)}
	h := operationcontrol.Helper{Command: filepath.Join(trial, "bin/bench-quota"), Policy: "/etc/superbench/storage.json", State: filepath.Join(trial, "state"), StateDevice: 1, StateInode: 2, Kind: "bench", Key: "materializing", Requested: 32 << 20}
	h.Identity = quotaoperation.Identity{OperationID: target.ID(), Epoch: strings.Repeat("b", 32), StateIdentity: h.StateIdentity(), Authority: strings.Repeat("c", 64), BinarySHA256: strings.Repeat("d", 64)}
	raw, _ := json.Marshal(h)
	args := []driver.Value{int64(1001), target.RequestID, target.Digest, h.Identity.Epoch, string(raw)}
	for _, at := range []int{0, 1, 2, 3, 4} {
		bad := append([]driver.Value(nil), args...)
		bad[at] = "wrong"
		if _, err := managedCancellationGate(trial, bad, time.Second); err == nil {
			t.Fatalf("argument %d accepted", at)
		}
		b, _ := os.ReadFile(marker)
		if len(b) != 0 {
			t.Fatal("invalid invocation published marker")
		}
	}
	done := make(chan error, 1)
	go func() { _, err := managedCancellationGate(trial, args, time.Second); done <- err }()
	deadline := time.Now().Add(time.Second)
	for {
		b, _ := os.ReadFile(marker)
		if string(b) == string(raw) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("marker not published")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("gate returned before exact release: %v", err)
	default:
	}
	if err := os.WriteFile(filepath.Join(dir, "release.json"), []byte("wrong"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("wrong release accepted")
	}
	if err := os.WriteFile(marker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "release.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := managedCancellationGate(trial, args, time.Second); err != nil {
		t.Fatal(err)
	}
}
