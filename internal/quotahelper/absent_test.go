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

func TestAbsentResultBindsTheWorkspace(t *testing.T) {
	id := strings.Repeat("c", 32)
	good := AbsentResult{AbsentVersion, "card", id, 2}
	raw, _ := json.Marshal(good)
	if got, err := ParseAbsentResult(raw, "card", id, 2); err != nil || got != good {
		t.Fatalf("result: %+v %v", got, err)
	}
	for name, change := range map[string]func(*AbsentResult){
		"version":    func(r *AbsentResult) { r.Version = RetiredVersion },
		"key":        func(r *AbsentResult) { r.Key = "other" },
		"workspace":  func(r *AbsentResult) { r.ID = strings.Repeat("d", 32) },
		"generation": func(r *AbsentResult) { r.Generation = 3 },
	} {
		r := good
		change(&r)
		if !errors.Is(r.Validate("card", id, 2), errAbsent) {
			t.Errorf("%s result accepted", name)
		}
	}
	retired, _ := json.Marshal(RetiredResult{RetiredVersion, "card", id, 2, Assignment{Path: "/srv/data/benches/card", ProjectID: 10004}})
	if _, err := ParseAbsentResult(retired, "card", id, 2); err == nil {
		t.Fatal("a retired reply accepted as absence")
	}
	t.Log("CAPACITY_REFUSED_ABSENT_REPLY_BOUND_PASS")
}

// The daemon asks the read-only absence step with only the key and the exact
// Spec; a reply for another workspace or generation is refused.
func TestAbsentTransportSendsOnlyTheKeyAndSpec(t *testing.T) {
	_, c, _, commands := scopedFixture(t)
	args := filepath.Join(filepath.Dir(c.Command), "absent-args")
	input := filepath.Join(filepath.Dir(c.Command), "absent-input")
	id := strings.Repeat("c", 32)
	reply := AbsentResult{AbsentVersion, "card", id, 2}
	raw, _ := json.Marshal(reply)
	t.Setenv("ABSENT_TEST_ARGS", args)
	t.Setenv("ABSENT_TEST_INPUT", input)
	t.Setenv("ABSENT_TEST_REPLY", string(raw))
	script, err := os.ReadFile(c.Command)
	if err != nil {
		t.Fatal(err)
	}
	script = []byte(strings.Replace(string(script), "apply-operation)", `inspect-absent-payload) printf '%s\n' "$@" >> "$ABSENT_TEST_ARGS"; /bin/cat > "$ABSENT_TEST_INPUT"; printf '%s' "$ABSENT_TEST_REPLY" ;;
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
	got, err := absentScoped(context.Background(), c, "card", id, 2, spec, 0, uint32(os.Geteuid()))
	if err != nil || got != reply {
		t.Fatalf("absent: %+v %v", got, err)
	}
	if raw, err := os.ReadFile(args); err != nil || string(raw) != "inspect-absent-payload\ncard\n" {
		t.Fatalf("arguments: %q %v", raw, err)
	}
	if raw, err := os.ReadFile(input); err != nil || string(raw) != string(spec) {
		t.Fatalf("spec: %q %v", raw, err)
	}
	if log, err := os.ReadFile(commands); err != nil || string(log) != "operation-info\ninspect-absent-payload\n" {
		t.Fatalf("commands: %q %v", log, err)
	}
	for name, ask := range map[string]func() error{
		"another workspace": func() error {
			_, err := absentScoped(context.Background(), c, "card", strings.Repeat("d", 32), 2, spec, 0, uint32(os.Geteuid()))
			return err
		},
		"another generation": func() error {
			_, err := absentScoped(context.Background(), c, "card", id, 3, spec, 0, uint32(os.Geteuid()))
			return err
		},
		"a policy override": func() error {
			o := c
			o.PolicyOverride = "/tmp/policy.json"
			_, err := absentScoped(context.Background(), o, "card", id, 2, spec, 0, uint32(os.Geteuid()))
			return err
		},
	} {
		if ask() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
