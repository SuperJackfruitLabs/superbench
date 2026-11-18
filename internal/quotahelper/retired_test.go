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
)

func TestRetiredResultBindsTheWorkspaceAndItsTombstone(t *testing.T) {
	data, id := "/srv/data", strings.Repeat("c", 32)
	good := RetiredResult{RetiredVersion, "card", id, 2, Assignment{Path: filepath.Join(data, "benches", "card"), ProjectID: 10004}}
	raw, _ := json.Marshal(good)
	if got, err := ParseRetiredResult(raw, data, "card", id, 2); err != nil || got != good {
		t.Fatalf("result: %+v %v", got, err)
	}
	for name, change := range map[string]func(*RetiredResult){
		"version":    func(r *RetiredResult) { r.Version = ReclaimVersion },
		"key":        func(r *RetiredResult) { r.Key = "other" },
		"workspace":  func(r *RetiredResult) { r.ID = strings.Repeat("d", 32) },
		"generation": func(r *RetiredResult) { r.Generation = 3 },
		"path":       func(r *RetiredResult) { r.Allocation.Path = "/srv/data/benches/other" },
		"shared":     func(r *RetiredResult) { r.Allocation.ProjectID = 101 },
		"bytes":      func(r *RetiredResult) { r.Allocation.Bytes = 1024 },
		"inodes":     func(r *RetiredResult) { r.Allocation.Inodes = 1 },
	} {
		r := good
		change(&r)
		if !errors.Is(r.Validate(data, "card", id, 2), errRetired) {
			t.Errorf("%s result accepted", name)
		}
	}
	notID := good
	notID.ID = "card"
	if !errors.Is(notID.Validate(data, "card", "card", 2), errRetired) {
		t.Fatal("a workspace that is not an ID accepted")
	}
	if _, err := ParseRetiredResult([]byte(strings.Replace(string(raw), `"Key":"card",`, ``, 1)+``), data, "card", id, 2); err == nil {
		t.Fatal("an incomplete result accepted")
	}
	t.Log("FAILED_CREATE_RETIRED_REPLY_BOUND_PASS")
}

// The daemon asks the read-only step with only the key and the exact Spec;
// a reply for another workspace or a live allocation is refused.
func TestRetiredTransportSendsOnlyTheKeyAndSpec(t *testing.T) {
	_, c, _, commands := scopedFixture(t)
	args := filepath.Join(filepath.Dir(c.Command), "retired-args")
	input := filepath.Join(filepath.Dir(c.Command), "retired-input")
	id := strings.Repeat("c", 32)
	reply := RetiredResult{RetiredVersion, "card", id, 2, Assignment{Path: filepath.Join(c.Data, "benches", "card"), ProjectID: 10004}}
	raw, _ := json.Marshal(reply)
	t.Setenv("RETIRED_TEST_ARGS", args)
	t.Setenv("RETIRED_TEST_INPUT", input)
	t.Setenv("RETIRED_TEST_REPLY", string(raw))
	script, err := os.ReadFile(c.Command)
	if err != nil {
		t.Fatal(err)
	}
	script = []byte(strings.Replace(string(script), "apply-operation)", `inspect-retired-payload) printf '%s\n' "$@" >> "$RETIRED_TEST_ARGS"; /bin/cat > "$RETIRED_TEST_INPUT"; printf '%s' "$RETIRED_TEST_REPLY" ;;
apply-operation)`, 1))
	if err = os.WriteFile(c.Command, script, 0700); err != nil {
		t.Fatal(err)
	}
	var info Info
	if err = json.Unmarshal([]byte(os.Getenv("QUOTA_SCOPED_INFO")), &info); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(script)
	info.BinarySHA256 = hex.EncodeToString(sum[:])
	rawInfo, _ := json.Marshal(info)
	t.Setenv("QUOTA_SCOPED_INFO", string(rawInfo))
	spec := []byte(`{"bounded":"spec"}`)
	got, err := retiredScoped(context.Background(), c, "card", id, 2, spec, 0, uint32(os.Geteuid()))
	if err != nil || got != reply {
		t.Fatalf("retired: %+v %v", got, err)
	}
	if raw, err := os.ReadFile(args); err != nil || string(raw) != "inspect-retired-payload\ncard\n" {
		t.Fatalf("arguments: %q %v", raw, err)
	}
	if raw, err := os.ReadFile(input); err != nil || string(raw) != string(spec) {
		t.Fatalf("spec: %q %v", raw, err)
	}
	if log, err := os.ReadFile(commands); err != nil || string(log) != "operation-info\ninspect-retired-payload\n" {
		t.Fatalf("commands: %q %v", log, err)
	}
	for name, ask := range map[string]func() error{
		"another workspace": func() error {
			_, err := retiredScoped(context.Background(), c, "card", strings.Repeat("d", 32), 2, spec, 0, uint32(os.Geteuid()))
			return err
		},
		"another generation": func() error {
			_, err := retiredScoped(context.Background(), c, "card", id, 3, spec, 0, uint32(os.Geteuid()))
			return err
		},
		"a policy override": func() error {
			o := c
			o.PolicyOverride = "/tmp/policy.json"
			_, err := retiredScoped(context.Background(), o, "card", id, 2, spec, 0, uint32(os.Geteuid()))
			return err
		},
	} {
		if ask() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
