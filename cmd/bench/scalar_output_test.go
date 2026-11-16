package main

import (
	"encoding/json"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLocalScalarPresentationKeepsPreRegistrationShape(t *testing.T) {
	b := &bench.Bench{Key: "card", Repo: "repo", Branch: "task", Path: "/data/benches/card/worktree", Mirror: "mirror", Checkpoint: "sealed", Lease: &bench.Lease{Holder: "owner", Token: "existing-grant"}}
	encode := func(v any) string {
		t.Helper()
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	old := encode(b)
	if encode(scalarOutput(b)) != old {
		t.Fatal("legacy scalar fields changed")
	}
	b.RegistrationID = "0123456789abcdef0123456789abcdef"
	b.PayloadLayoutVersion = "superbench.payload-layout.v1"
	b.AllocationRoot = "/data/benches/card"
	if encode(scalarOutput(b)) != old {
		t.Fatal("private authority leaked into scalar CLI JSON")
	}
	for _, v := range []any{[]bench.Bench{*b}, &bench.RunResult{Bench: b}} {
		raw := encode(scalarOutput(v))
		var fields any
		if err := json.Unmarshal([]byte(raw), &fields); err != nil {
			t.Fatal(err)
		}
		if containsPrivate(fields) {
			t.Fatal("nested output leaked storage authority", raw)
		}
	}
	if encode(b) == old {
		t.Fatal("presentation mutated durable storage")
	}
}
func containsPrivate(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, y := range x {
			if k == "RegistrationID" || k == "PayloadLayoutVersion" || k == "AllocationRoot" || containsPrivate(y) {
				return true
			}
		}
	case []any:
		for _, y := range x {
			if containsPrivate(y) {
				return true
			}
		}
	}
	return false
}

// Keep the public field inventory and JSON tags aligned with durable Bench.
// Interface-valued presentation fields must accept the original concrete type.
func TestScalarRecordCoversEveryPublicBenchField(t *testing.T) {
	private := map[string]bool{"RegistrationID": true, "PayloadLayoutVersion": true, "AllocationRoot": true}
	source, view := reflect.TypeOf(model.Bench{}), reflect.TypeOf(scalarRecord{})
	public := 0
	for i := 0; i < source.NumField(); i++ {
		f := source.Field(i)
		if private[f.Name] {
			if _, ok := view.FieldByName(f.Name); ok {
				t.Errorf("private field exposed: %s", f.Name)
			}
			continue
		}
		public++
		v, ok := view.FieldByName(f.Name)
		if !ok || v.Tag != f.Tag || !f.Type.AssignableTo(v.Type) {
			t.Errorf("public field lost or changed: %s", f.Name)
			continue
		}
		record := &model.Bench{}
		field := reflect.ValueOf(record).Elem().Field(i)
		switch field.Kind() {
		case reflect.String:
			field.SetString("public-value")
		case reflect.Uint32, reflect.Uint64:
			field.SetUint(123)
		case reflect.Pointer:
			field.Set(reflect.New(field.Type().Elem()))
		default:
			t.Fatalf("add a representative value for new public field %s (%s)", f.Name, f.Type)
		}
		copied := reflect.ValueOf(scalarView(record)).Elem().FieldByName(f.Name)
		if !reflect.DeepEqual(field.Interface(), copied.Interface()) {
			t.Errorf("public field not copied: %s", f.Name)
		}
	}
	if view.NumField() != public {
		t.Fatalf("presentation field count=%d want=%d", view.NumField(), public)
	}
}

func TestRunScalarStatusOmitsMigratedRegistration(t *testing.T) {
	root := t.TempDir()
	state, data := filepath.Join(root, "state"), filepath.Join(root, "data")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	b := bench.Bench{Key: "card", Repo: "repo", Branch: "task", Path: filepath.Join(data, "card"), Mirror: filepath.Join(data, "mirror"), Checkpoint: "sealed"}
	raw, err := json.Marshal(map[string]bench.Bench{"card": b})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "benches.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	invoke := func(operation string, key ...string) []byte {
		t.Helper()
		// File-backed capture avoids a pipe capacity dependency and restores the
		// process-global output even on a failing assertion. These tests aren't parallel.
		file, err := os.CreateTemp(t.TempDir(), "stdout")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		prior := os.Stdout
		os.Stdout = file
		defer func() { os.Stdout = prior }()
		args := append([]string{operation, "--state", state, "--data", data}, key...)
		if err := run(args); err != nil {
			t.Fatal(err)
		}
		if _, err := file.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
		out, err := io.ReadAll(file)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	invoke("migrate")
	rows, err := (bench.Store{State: state, Data: data}).Status("card")
	if err != nil || len(rows) != 1 || len(rows[0].RegistrationID) != 32 {
		t.Fatalf("fixture not durably migrated: %+v %v", rows, err)
	}
	for _, key := range [][]string{nil, {"card"}} {
		out := invoke("status", key...)
		var got []map[string]json.RawMessage
		if err := json.Unmarshal(out, &got); err != nil || len(got) != 1 {
			t.Fatalf("invalid CLI result: %s %v", out, err)
		}
		expected, err := json.Marshal(scalarOutput(rows))
		if err != nil {
			t.Fatal(err)
		}
		var want any
		var actual any
		if err := json.Unmarshal(expected, &want); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(out, &actual); err != nil {
			t.Fatal(err)
		}
		if containsPrivate(actual) {
			t.Fatalf("run leaked private authority: %s", out)
		}
		if !reflect.DeepEqual(actual, want) {
			t.Fatalf("CLI lost public fields: %s", out)
		}
	}
}
