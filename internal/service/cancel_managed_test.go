package service

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
)

// The ordinary daemon cannot enable this proposed route. This opt-in test
// process is only a disposable managed acceptance fixture, never an installer.
func TestManagedCancellationDaemon(t *testing.T) {
	trial := os.Getenv("SUPERBENCH_CANCELLATION_TRIAL")
	if trial == "" {
		t.Skip("opt-in owned Linux managed cancellation fixture")
	}
	if runtime.GOOS != "linux" || os.Getuid() != 0 || os.Geteuid() != 0 || os.Getenv("SUDO_UID") != "" {
		t.Fatal("direct root Linux fixture required")
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	config := Config{Socket: filepath.Join(trial, "api.sock"), Store: bench.Store{State: filepath.Join(trial, "state"), Data: filepath.Join(trial, "data"), CgroupRoot: root}, Operators: map[uint32]bool{0: true}, MaxActive: 64}
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
