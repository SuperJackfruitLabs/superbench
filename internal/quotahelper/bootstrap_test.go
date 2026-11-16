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
)

func bootstrapTransportFixture(t *testing.T) (context.Context, Config, *[]operationcontrol.Helper, string, string) {
	t.Helper()
	ctx, c, recorded, commands := scopedFixture(t)
	inputPath := filepath.Join(filepath.Dir(c.Command), "bootstrap-input")
	t.Setenv("BOOTSTRAP_TEST_INPUT", inputPath)
	script, err := os.ReadFile(c.Command)
	if err != nil {
		t.Fatal(err)
	}
	script = []byte(strings.Replace(string(script), "apply-operation)", "bootstrap-payload) test -f \"$QUOTA_SCOPED_INTENT\" || exit 19; /bin/cat > \"$BOOTSTRAP_TEST_INPUT\"; printf '%s' '{\"observed\":true}' ;;\napply-operation)", 1))
	if err = os.WriteFile(c.Command, script, 0700); err != nil {
		t.Fatal(err)
	}
	var info Info
	if err = json.Unmarshal([]byte(os.Getenv("QUOTA_SCOPED_INFO")), &info); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(script)
	info.BinarySHA256 = hex.EncodeToString(sum[:])
	raw, _ := json.Marshal(info)
	t.Setenv("QUOTA_SCOPED_INFO", string(raw))
	return ctx, c, recorded, commands, inputPath
}

func TestBootstrapTransportRecordsBeforeFiniteInputLaunch(t *testing.T) {
	ctx, c, recorded, commands, inputPath := bootstrapTransportFixture(t)
	input := []byte("{\"bounded\":\"input\"}")
	got, err := bootstrapScoped(ctx, c, "card", 4096, input, 0, uint32(os.Geteuid()))
	if err != nil || string(got) != "{\"observed\":true}" || len(*recorded) != 1 {
		t.Fatal(string(got), *recorded, err)
	}
	actual, err := os.ReadFile(inputPath)
	if err != nil || string(actual) != string(input) {
		t.Fatal(string(actual), err)
	}
	log, err := os.ReadFile(commands)
	if err != nil || string(log) != "operation-info\nbootstrap-payload\n" {
		t.Fatal(string(log), err)
	}
	i, _ := operationcontrol.FromContext(ctx)
	h := (*recorded)[0]
	if !h.Valid(i) || h.Kind != "bench" || h.Key != "card" || h.Requested != 4096 {
		t.Fatal(h)
	}
}

func TestBootstrapTransportRefusesMissingOrConflictingAuthorityBeforeChild(t *testing.T) {
	ctx, c, _, commands, _ := bootstrapTransportFixture(t)
	for _, test := range []struct {
		ctx      context.Context
		key      string
		input    []byte
		override bool
	}{
		{context.Background(), "card", []byte("{}"), false},
		{ctx, "foreign", []byte("{}"), false},
		{ctx, "card", nil, false},
		{ctx, "card", make([]byte, MaxReply+1), false},
		{ctx, "card", []byte("{}"), true},
	} {
		configured := c
		if test.override {
			configured.PolicyOverride = "/untrusted"
		}
		if _, err := bootstrapScoped(test.ctx, configured, test.key, 4096, test.input, 0, uint32(os.Geteuid())); !errors.Is(err, operationcontrol.ErrAuthority) {
			t.Fatalf("wrong refusal: %v", err)
		}
	}
	if _, err := os.Stat(commands); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("child crossed preflight", err)
	}
}

func TestBootstrapTransportFailedRecorderPreventsInputAndEffects(t *testing.T) {
	ctx, c, _, commands, inputPath := bootstrapTransportFixture(t)
	i, _ := operationcontrol.FromContext(ctx)
	ctx, err := operationcontrol.WithScope(context.Background(), i, func(fn func() error) error { return fn() })
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = operationcontrol.WithHelperRecorder(ctx, func(operationcontrol.Helper) error { return os.ErrPermission })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = bootstrapScoped(ctx, c, "card", 4096, []byte("{}"), 0, uint32(os.Geteuid())); !errors.Is(err, os.ErrPermission) {
		t.Fatal(err)
	}
	log, err := os.ReadFile(commands)
	if err != nil || string(log) != "operation-info\n" {
		t.Fatal(string(log), err)
	}
	if _, err = os.Stat(inputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("input/effects after failed admission", err)
	}
}
