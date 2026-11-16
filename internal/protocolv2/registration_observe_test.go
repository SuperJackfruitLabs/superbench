package protocolv2

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func observationFixture(t *testing.T) (RegistrationObserveRequest, RegistrationObserveResponse, []byte) {
	t.Helper()
	r := RegistrationObserveRequest{Version: Version, Operation: "registration_observe", Key: "card", BenchID: strings.Repeat("a", 32), Holder: "fixture:owner", LeaseToken: strings.Repeat("b", 32)}
	n := func(inode string) RegistrationNode { return RegistrationNode{Device: "1", Inode: inode} }
	v := &RegistrationObservation{Key: r.Key, BenchID: r.BenchID, RootPath: "/data/benches/card", LayoutVersion: "superbench.payload-layout.v1", PolicySHA256: strings.Repeat("c", 64), Root: n("1"), RootHome: n("2"), RootTemp: n("3"), Members: []RegistrationMember{{Name: "app", Checkout: n("4"), Admin: n("5"), Home: n("6"), Temp: n("7")}}, Checkpoint: RegistrationCheckpoint{GenerationID: strings.Repeat("d", 32), ManifestSHA256: strings.Repeat("e", 64), Seal: strings.Repeat("f", 40), Members: []Commit{{Name: "app", Commit: strings.Repeat("a", 40)}}}}
	response := RegistrationObserveResponse{Version: Version, Operation: r.Operation, OK: true, Result: v}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ParseRegistrationObserveResponse(raw, 200, r); err != nil {
		t.Fatalf("valid observation fixture refused: %v", err)
	}
	return r, response, raw
}

func TestRegistrationObserveRequestIsStrictSeparateAndBounded(t *testing.T) {
	r, _, _ := observationFixture(t)
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ParseRegistrationObserveRequest(raw); err != nil || got != r {
		t.Fatalf("valid request changed: %#v %v", got, err)
	}
	if _, err = Parse(raw); !errors.Is(err, ErrInvalid) {
		t.Fatalf("observation entered ordinary operation union: %v", err)
	}
	for _, candidate := range [][]byte{
		bytes.Replace(raw, []byte(`"key":"card"`), []byte(`"key":"card","key":"card"`), 1),
		bytes.Replace(raw, []byte(`"key":"card"`), []byte(`"key":null`), 1),
		bytes.Replace(raw, []byte(`"key":"card",`), nil, 1),
		append(bytes.Clone(raw[:len(raw)-1]), []byte(`,"scope":"fake"}`)...),
		append(bytes.Repeat([]byte(" "), MaxBody), raw...),
		bytes.Replace(raw, []byte(r.LeaseToken), []byte(strings.Repeat("B", 32)), 1),
	} {
		if _, err = ParseRegistrationObserveRequest(candidate); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unsafe request accepted: %v", err)
		}
	}
}

func TestRegistrationObserveResponseJoinsCompletePrivateVector(t *testing.T) {
	r, _, raw := observationFixture(t)
	for name, change := range map[string]func(*RegistrationObserveResponse){
		"key":               func(s *RegistrationObserveResponse) { s.Result.Key = "later" },
		"bid":               func(s *RegistrationObserveResponse) { s.Result.BenchID = strings.Repeat("f", 32) },
		"shared-node":       func(s *RegistrationObserveResponse) { s.Result.Members[0].Admin = s.Result.Root },
		"foreign-device":    func(s *RegistrationObserveResponse) { s.Result.Members[0].Admin.Device = "2" },
		"uint64-overflow":   func(s *RegistrationObserveResponse) { s.Result.Root.Inode = "18446744073709551616" },
		"noncanonical-node": func(s *RegistrationObserveResponse) { s.Result.Root.Inode = "01" },
		"missing-member":    func(s *RegistrationObserveResponse) { s.Result.Members = nil },
		"checkpoint-member": func(s *RegistrationObserveResponse) { s.Result.Checkpoint.Members[0].Name = "other" },
		"extra-checkpoint-member": func(s *RegistrationObserveResponse) {
			s.Result.Checkpoint.Members = append(s.Result.Checkpoint.Members, Commit{Name: "other", Commit: strings.Repeat("a", 40)})
		},
		"result-with-error": func(s *RegistrationObserveResponse) { s.Error = &APIError{Code: "unavailable", Message: "refused"} },
		"oversize-root":     func(s *RegistrationObserveResponse) { s.Result.RootPath = "/" + strings.Repeat("x", 4096) },
	} {
		t.Run(name, func(t *testing.T) {
			var s RegistrationObserveResponse
			if err := json.Unmarshal(raw, &s); err != nil {
				t.Fatal(err)
			}
			change(&s)
			if err := ValidateRegistrationObserveResponse(s, 200, r); !errors.Is(err, ErrResponse) {
				t.Fatalf("mismatched observation accepted: %v", err)
			}
		})
	}
	for _, candidate := range [][]byte{
		bytes.Replace(raw, []byte(`"device":"1"`), []byte(`"device":"1","device":"1"`), 1),
		bytes.Replace(raw, []byte(`"root":{"device":"1","inode":"1"}`), []byte(`"root":{"device":"1","inode":"1","uid":1002}`), 1),
		append(bytes.Clone(raw[:len(raw)-1]), []byte(`,"grant":{"lease_token":"private"}}`)...),
	} {
		if _, err := ParseRegistrationObserveResponse(candidate, 200, r); !errors.Is(err, ErrResponse) {
			t.Fatalf("unknown/duplicate observation field accepted: %v", err)
		}
	}
	boundary := append(bytes.Repeat([]byte(" "), MaxBody-len(raw)), raw...)
	if _, err := ParseRegistrationObserveResponse(boundary, 200, r); err != nil {
		t.Fatalf("exact serialized byte bound refused: %v", err)
	}
	if _, err := ParseRegistrationObserveResponse(append(boundary, ' '), 200, r); !errors.Is(err, ErrResponse) {
		t.Fatalf("whole-body byte bound ignored: %v", err)
	}
}
