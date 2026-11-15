package quotahelper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

func scopedFixture(t *testing.T) (context.Context, Config, *[]operationcontrol.Helper, string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "state")
	if err = os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	c := Config{State: state, Data: filepath.Join(dir, "data"), Command: filepath.Join(dir, "helper")}
	record := filepath.Join(dir, "commands")
	script := `#!/bin/sh
if [ "$1" = --policy ]; then shift 2; fi
printf '%s\n' "$1" >> "$QUOTA_SCOPED_RECORD"
case "$1" in
operation-info) printf '%s' "$QUOTA_SCOPED_INFO" ;;
apply-operation) test -f "$QUOTA_SCOPED_INTENT" || exit 19; printf '{"Version":"superbench.quota-operation.v1","Identity":{"OperationID":"%s","Epoch":"%s","StateIdentity":"%s","Authority":"%s","BinarySHA256":"%s"},"Assignment":%s}' "$5" "$6" "$7" "$8" "$9" "$QUOTA_SCOPED_ASSIGNMENT" ;;
cancel-operation) printf '%s' "$QUOTA_SCOPED_CANCEL" ;;
*) exit 17 ;;
esac
`
	if err = os.WriteFile(c.Command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(script))
	info := Info{Version: quotaoperation.Version, Root: c.Data, Authority: strings.Repeat("c", 64), BinarySHA256: hex.EncodeToString(sum[:])}
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("QUOTA_SCOPED_INTENT", filepath.Join(dir, "intent"))
	t.Setenv("QUOTA_SCOPED_RECORD", record)
	t.Setenv("QUOTA_SCOPED_INFO", string(raw))
	assignment := Assignment{Path: filepath.Join(c.Data, "benches", "card"), ProjectID: 10000, Bytes: 4096, Inodes: 9}
	raw, err = json.Marshal(assignment)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("QUOTA_SCOPED_ASSIGNMENT", string(raw))
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 7, RequestID: "typed", Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "create", Epoch: strings.Repeat("b", 32)}
	ctx, err := operationcontrol.WithScope(context.Background(), i, func(fn func() error) error { return fn() })
	if err != nil {
		t.Fatal(err)
	}
	var recorded []operationcontrol.Helper
	ctx, err = operationcontrol.WithHelperRecorder(ctx, func(h operationcontrol.Helper) error {
		recorded = append(recorded, h)
		return os.WriteFile(filepath.Join(dir, "intent"), []byte("committed"), 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, c, &recorded, record
}
func TestScopedApplyRecordsOriginalAuthorityBeforeTypedLaunch(t *testing.T) {
	ctx, c, recorded, record := scopedFixture(t)
	got, err := applyScoped(ctx, c, "bench", "card", 4096, 0, uint32(os.Geteuid()))
	if err != nil || got.ProjectID != 10000 || len(*recorded) != 1 {
		t.Fatal(got, *recorded, err)
	}
	h := (*recorded)[0]
	i, _ := operationcontrol.FromContext(ctx)
	if !h.Valid(i) || h.State != c.State || h.Command != c.Command || h.Requested != 4096 || h.Policy != "/etc/superbench/storage.json" {
		t.Fatal(h)
	}
	commands, err := os.ReadFile(record)
	if err != nil || string(commands) != "operation-info\napply-operation\n" {
		t.Fatal(string(commands), err)
	}
}
func TestScopedApplyRefusesInfoMismatchOrFailedJournalBeforeQuotaEffects(t *testing.T) {
	for _, fault := range []string{"root", "binary", "journal", "unknown", "oversize"} {
		t.Run(fault, func(t *testing.T) {
			ctx, c, _, record := scopedFixture(t)
			if fault == "journal" {
				i, _ := operationcontrol.FromContext(ctx)
				ctx, _ = operationcontrol.WithScope(context.Background(), i, func(fn func() error) error { return fn() })
				ctx, _ = operationcontrol.WithHelperRecorder(ctx, func(operationcontrol.Helper) error { return os.ErrPermission })
			} else {
				var info map[string]any
				if err := json.Unmarshal([]byte(os.Getenv("QUOTA_SCOPED_INFO")), &info); err != nil {
					t.Fatal(err)
				}
				switch fault {
				case "root":
					info["Root"] = "/different"
				case "binary":
					info["BinarySHA256"] = strings.Repeat("f", 64)
				case "unknown":
					info["Extra"] = true
				case "oversize":
					info["Extra"] = strings.Repeat("x", MaxReply)
				}
				raw, err := json.Marshal(info)
				if err != nil {
					t.Fatal(err)
				}
				t.Setenv("QUOTA_SCOPED_INFO", string(raw))
			}
			if _, err := applyScoped(ctx, c, "bench", "card", 4096, 0, uint32(os.Geteuid())); err == nil {
				t.Fatal("untracked or mismatched helper applied")
			}
			commands, err := os.ReadFile(record)
			if err != nil || string(commands) != "operation-info\n" {
				t.Fatal("quota effects crossed rejected info/intent", string(commands), err)
			}
		})
	}
}

func TestCancellationBetweenHelperJournalAndStartPreservesIntentWithoutMutation(t *testing.T) {
	initial, c, recorded, record := scopedFixture(t)
	i, _ := operationcontrol.FromContext(initial)
	cancelled := false
	denied := errors.New("durable cancellation won after journal")
	ctx, err := operationcontrol.WithScope(context.Background(), i, func(fn func() error) error {
		if cancelled {
			return denied
		}
		return fn()
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = operationcontrol.WithHelperRecorder(ctx, func(h operationcontrol.Helper) error {
		*recorded = append(*recorded, h)
		if err := os.WriteFile(os.Getenv("QUOTA_SCOPED_INTENT"), []byte("committed"), 0600); err != nil {
			return err
		}
		cancelled = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = applyScoped(ctx, c, "bench", "card", 4096, 0, uint32(os.Geteuid())); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	commands, err := os.ReadFile(record)
	if err != nil || string(commands) != "operation-info\n" || len(*recorded) != 1 {
		t.Fatal("helper crossed late cancellation", string(commands), len(*recorded), err)
	}
}

func TestStateRootReplacementAfterIntentRefusesHelperLaunch(t *testing.T) {
	initial, c, _, record := scopedFixture(t)
	i, _ := operationcontrol.FromContext(initial)
	ctx, err := operationcontrol.WithScope(context.Background(), i, func(fn func() error) error { return fn() })
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = operationcontrol.WithHelperRecorder(ctx, func(operationcontrol.Helper) error {
		if err := os.Rename(c.State, c.State+"-evidence"); err != nil {
			return err
		}
		if err := os.Mkdir(c.State, 0700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(c.State, "sentinel"), []byte("preserve"), 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = applyScoped(ctx, c, "bench", "card", 4096, 0, uint32(os.Geteuid())); err == nil {
		t.Fatal("replaced state namespace authorized allocation")
	}
	commands, err := os.ReadFile(record)
	if err != nil || string(commands) != "operation-info\n" {
		t.Fatal(string(commands), err)
	}
	raw, err := os.ReadFile(filepath.Join(c.State, "sentinel"))
	if err != nil || string(raw) != "preserve" {
		t.Fatal("replacement bytes changed", err)
	}
	if _, err = os.Stat(c.State + "-evidence"); err != nil {
		t.Fatal("original state was removed", err)
	}
}
