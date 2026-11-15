package service

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"modernc.org/sqlite"
)

// Only the opt-in test daemon registers this function. A narrowly scoped fixture
// trigger pauses the bench-helper intent before INSERT commits, after the shared
// helpers and durable materializing owner. No shipped daemon registers it.
func installManagedCancellationGate(trial string) error {
	return sqlite.RegisterScalarFunction("superbench_cancellation_fixture_gate", 5, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		return managedCancellationGate(trial, args, 30*time.Second)
	})
}

func managedCancellationGate(trial string, args []driver.Value, timeout time.Duration) (driver.Value, error) {
	invalid := errors.New("invalid owned cancellation fixture gate")
	if len(args) != 5 || args[0] != int64(1001) || args[1] != "materialization-create" {
		return nil, invalid
	}
	digest, ok := args[2].(string)
	if !ok {
		return nil, invalid
	}
	epoch, ok := args[3].(string)
	if !ok {
		return nil, invalid
	}
	raw, ok := args[4].(string)
	if !ok || len(raw) > operationcontrol.MaxHelperIntent {
		return nil, invalid
	}
	intent := operationcontrol.Intent{Target: operationcontrol.Target{UID: 1001, RequestID: "materialization-create", Digest: digest}, Key: "materializing", Operation: "create", Epoch: epoch}
	var h operationcontrol.Helper
	if json.Unmarshal([]byte(raw), &h) != nil || !h.Valid(intent) || h.Kind != "bench" || h.Key != "materializing" || h.Command != filepath.Join(trial, "bin", "bench-quota") || h.Policy != "/etc/superbench/storage.json" || h.State != filepath.Join(trial, "state") {
		return nil, invalid
	}
	canonical, err := json.Marshal(h)
	if err != nil || !bytes.Equal(canonical, []byte(raw)) {
		return nil, invalid
	}
	marker := filepath.Join(trial, "cancellation-gate", "observed.json")
	release := filepath.Join(trial, "cancellation-gate", "release.json")
	f, err := os.OpenFile(marker, os.O_WRONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
		f.Close()
		return nil, invalid
	}
	if _, err = f.Write(canonical); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	deadline := time.Now().Add(timeout)
	for {
		f, err := os.OpenFile(release, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err == nil {
			info, statErr := f.Stat()
			if statErr != nil || !info.Mode().IsRegular() || info.Size() > operationcontrol.MaxHelperIntent {
				f.Close()
				return nil, invalid
			}
			value, readErr := io.ReadAll(io.LimitReader(f, operationcontrol.MaxHelperIntent+1))
			f.Close()
			if readErr != nil || !bytes.Equal(value, canonical) {
				return nil, invalid
			}
			return int64(1), nil
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, errors.New("owned cancellation fixture gate timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
