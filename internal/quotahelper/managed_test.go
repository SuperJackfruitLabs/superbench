package quotahelper_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
)

// This opt-in child is driven only in an owned mount namespace by the managed
// helper drill. It proves actual Worker/helper transport, not service or cgroup
// cancellation. The parent may kill this controller while its helper is queued.
func TestManagedQuotaOperationChild(t *testing.T) {
	root, mode := os.Getenv("BENCH_QUOTA_OPERATION_ROOT"), os.Getenv("BENCH_QUOTA_OPERATION_MODE")
	if root == "" || mode == "" {
		t.Skip("owned managed helper fixture required")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 || !filepath.IsAbs(root) || filepath.Clean(root) != root || !strings.HasPrefix(root, "/var/tmp/superbench-cancel-helper-") {
		t.Fatal("invalid owned helper fixture")
	}
	key := os.Getenv("BENCH_QUOTA_OPERATION_KEY")
	sum := sha256.Sum256([]byte("owned-helper-fixture:" + key))
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 0, RequestID: key, Digest: hex.EncodeToString(sum[:])}, Key: key, Operation: "create", Epoch: strings.Repeat("b", 32)}
	if !i.Valid() {
		t.Fatal("invalid fixture identity")
	}
	journal := filepath.Join(root, "intent-"+key+".json")
	if mode == "cancel" {
		raw, err := os.ReadFile(journal)
		if err != nil {
			t.Fatal(err)
		}
		var h operationcontrol.Helper
		if err = json.Unmarshal(raw, &h); err != nil {
			t.Fatal(err)
		}
		proof, err := quotahelper.Cancel(context.Background(), i, h)
		if err != nil {
			t.Fatal(err)
		}
		raw, err = json.Marshal(proof)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Printf("PROOF:%s\n", raw)
		return
	}
	if mode != "apply" {
		t.Fatal("unknown fixture mode")
	}
	ctx, err := operationcontrol.WithScope(context.Background(), i, func(fn func() error) error { return fn() })
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = operationcontrol.WithHelperRecorder(ctx, func(h operationcontrol.Helper) error {
		raw, e := json.Marshal(h)
		if e != nil {
			return e
		}
		f, e := os.OpenFile(journal, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if os.IsExist(e) {
			old, readErr := os.ReadFile(journal)
			if readErr != nil {
				return readErr
			}
			if string(old) != string(raw) {
				return fmt.Errorf("fixture intent rebound")
			}
			return nil
		}
		if e != nil {
			return e
		}
		defer f.Close()
		if _, e = f.Write(raw); e != nil {
			return e
		}
		if e = f.Sync(); e != nil {
			return e
		}
		dir, e := os.Open(root)
		if e != nil {
			return e
		}
		defer dir.Close()
		return dir.Sync()
	})
	if err != nil {
		t.Fatal(err)
	}
	worker := hoststorage.Worker{Context: ctx, State: filepath.Join(root, "state"), Data: filepath.Join(root, "data")}
	assignment, err := worker.Quota(hoststorage.HostConfig{QuotaHelper: filepath.Join(root, "bench-quota"), PolicyOverride: filepath.Join(root, "policy.json")}, "bench", key, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("ASSIGNMENT:%+v\n", assignment)
}
