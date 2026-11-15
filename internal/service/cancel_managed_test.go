package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
)

// The ordinary daemon cannot enable this proposed route. This opt-in test
// process is only a disposable managed acceptance fixture, never an installer.
func TestManagedCancellationDaemon(t *testing.T) {
	trial := os.Getenv("SUPERBENCH_CANCELLATION_TRIAL")
	if trial == "" {
		t.Skip("opt-in owned Linux managed cancellation fixture")
	}
	if runtime.GOOS != "linux" || os.Getuid() != 1001 || os.Geteuid() != 1001 || os.Getenv("SUDO_UID") != "" {
		t.Fatal("unprivileged UID1001 Linux fixture required")
	}
	if !strings.HasPrefix(trial, "/var/tmp/superbench-cancel-managed.") || filepath.Clean(trial) != trial {
		t.Fatal("invalid fixture root")
	}
	actual, err := filepath.EvalSymlinks(trial)
	if err != nil || actual != trial {
		t.Fatal("fixture root is not physical", err)
	}
	root := os.Getenv("SUPERBENCH_CANCELLATION_CGROUP_ROOT")
	if !strings.HasPrefix(root, "/sys/fs/cgroup/superbench-cancel-") || filepath.Clean(root) != root {
		t.Fatal("invalid fixture control root")
	}
	if err := installManagedCancellationGate(trial); err != nil {
		t.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	config := Config{Socket: filepath.Join(trial, "run", "api.sock"), Store: bench.Store{State: filepath.Join(trial, "state"), Data: filepath.Join(trial, "data"), CgroupRoot: root}, Operators: map[uint32]bool{1001: true}, MaxActive: 64}
	if err = serve(ctx, config, true); err != nil {
		t.Fatal(err)
	}
}

// The fixture derives its target through the actual typed producer digest,
// rather than a second implementation of canonicalization in the Python driver.
func TestManagedCancellationSourceDigest(t *testing.T) {
	path := os.Getenv("SUPERBENCH_CANCELLATION_DIGEST_FILE")
	if path == "" {
		t.Skip("opt-in fixture digest")
	}
	if !strings.HasPrefix(path, "/var/tmp/superbench-cancel-managed.") || filepath.Clean(path) != path {
		t.Fatal("invalid fixture source request")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > protocolv2.MaxBody {
		t.Fatal("invalid fixture source bytes", err)
	}
	raw := make([]byte, info.Size())
	if _, err = file.ReadAt(raw, 0); err != nil {
		t.Fatal(err)
	}
	request, err := protocolv2.Parse(raw)
	if err != nil {
		t.Fatal("invalid fixture request")
	}
	target, err := protocolv2.NewCancelRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("CANCELLATION_DIGEST=" + target.TargetDigest)
}

// Diagnostic-only direct dispatch exposes no request, result or capability. It
// narrows fixture startup failures before claiming any wire/runtime acceptance.
func TestManagedCancellationDiagnostic(t *testing.T) {
	path := os.Getenv("SUPERBENCH_CANCELLATION_DIGEST_FILE")
	if os.Getenv("SUPERBENCH_CANCELLATION_DIAGNOSTIC_ONLY") != "1" {
		t.Skip("opt-in fixture diagnosis")
	}
	trial, root := os.Getenv("SUPERBENCH_CANCELLATION_TRIAL"), os.Getenv("SUPERBENCH_CANCELLATION_CGROUP_ROOT")
	if runtime.GOOS != "linux" || os.Getuid() != 1001 || os.Geteuid() != 1001 || filepath.Clean(trial) != trial || !strings.HasPrefix(trial, "/var/tmp/superbench-cancel-managed.") || !strings.HasPrefix(root, "/sys/fs/cgroup/superbench-cancel-") || path != filepath.Join(trial, "source-request.json") {
		t.Fatal("invalid diagnostic fixture")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal("diagnostic input unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > protocolv2.MaxBody {
		t.Fatal("invalid diagnostic input")
	}
	raw := make([]byte, info.Size())
	if _, err = file.ReadAt(raw, 0); err != nil {
		t.Fatal("diagnostic input incomplete")
	}
	request, err := protocolv2.Parse(raw)
	if err != nil || request.Operation != "create" {
		t.Fatal("invalid diagnostic request")
	}
	ledger, err := openReceipts(filepath.Join(trial, "state"))
	if err != nil {
		t.Fatal("diagnostic ledger unavailable")
	}
	defer ledger.Close()
	digest, err := request.Digest()
	if err != nil {
		t.Fatal("diagnostic digest unavailable")
	}
	intent, err := newOperationIntent(request, 1001, digest)
	if err != nil {
		t.Fatal("diagnostic intent unavailable")
	}
	cached, ctx, stop, err := ledger.ClaimScope(context.Background(), intent)
	if err != nil || cached != nil || stop == nil {
		t.Fatal("fresh diagnostic admission required")
	}
	defer stop()
	s := Server{config: Config{Store: bench.Store{State: filepath.Join(trial, "state"), Data: filepath.Join(trial, "data"), CgroupRoot: root}}, receipts: ledger}
	_, _, err = s.dispatchV2(ctx, request, 1001)
	report := map[string]any{"success": err == nil, "permission": errors.Is(err, os.ErrPermission), "not_exist": errors.Is(err, os.ErrNotExist), "authority": errors.Is(err, operationcontrol.ErrAuthority)}
	var types []string
	queue := []error{err}
	for len(queue) > 0 && len(types) < 32 {
		current := queue[0]
		queue = queue[1:]
		if current == nil {
			continue
		}
		types = append(types, fmt.Sprintf("%T", current))
		if joined, ok := current.(interface{ Unwrap() []error }); ok {
			queue = append(queue, joined.Unwrap()...)
		} else {
			queue = append(queue, errors.Unwrap(current))
		}
	}
	report["types"] = types
	if err != nil {
		sum := sha256.Sum256([]byte(err.Error()))
		report["error_sha256"] = hex.EncodeToString(sum[:])
	}
	encoded, _ := json.Marshal(report)
	fmt.Println("CANCELLATION_DIAGNOSTIC=" + string(encoded))
}
