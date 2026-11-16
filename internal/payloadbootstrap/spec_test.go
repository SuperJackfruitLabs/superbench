package payloadbootstrap

import (
	"errors"
	"strings"
	"testing"
)

func validSpecForTest() Spec {
	return Spec{Version: Version, Key: "card", CheckpointRepository: "api", ID: strings.Repeat("a", 32), Members: []Member{{Name: "api", Repository: "https://example.invalid/repo.git", Branch: "card/task", Source: strings.Repeat("b", 40), ObjectFormat: "sha1"}}}
}
func TestFreshSpecificationRejectsAmbiguousOrNoncanonicalAuthority(t *testing.T) {
	valid := validSpecForTest()
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Spec){
		func(s *Spec) { s.ID = "card" }, func(s *Spec) { s.Key = "../card" },
		func(s *Spec) { s.Members[0].Name = ".superbench-git" },
		func(s *Spec) { s.Members[0].Source = "HEAD" }, func(s *Spec) { s.Members[0].ObjectFormat = "sha256" },
		func(s *Spec) { s.Members[0].Branch = "--upload-pack=bad" },
		func(s *Spec) { s.Members = append(s.Members, s.Members[0]) },
	} {
		s := valid
		s.Members = append([]Member(nil), valid.Members...)
		mutate(&s)
		if err := s.Validate(); !errors.Is(err, ErrBootstrap) {
			t.Fatalf("accepted invalid spec %#v: %v", s, err)
		}
	}
	raw, err := valid.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ParseSpec(append(raw, []byte(` {}`)...)); !errors.Is(err, ErrBootstrap) {
		t.Fatalf("trailing document: %v", err)
	}
	if _, err = ParseSpec([]byte(strings.Replace(string(raw), `"Version":`, `"Unknown":0,"Version":`, 1))); !errors.Is(err, ErrBootstrap) {
		t.Fatalf("unknown property: %v", err)
	}
}

func TestFreshSpecificationCodecBindsCanonicalVector(t *testing.T) {
	valid := validSpecForTest()
	raw, err := valid.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseSpec(raw)
	if err != nil || got.ID != valid.ID || len(got.Members) != 1 {
		t.Fatalf("valid specification refused: %#v %v", got, err)
	}
	// The file parser deliberately permits formatting whitespace, but not a
	// second representation with unknown, duplicate or reordered authority fields.
	if _, err := ParseSpec(append([]byte(" \n"), append(raw, '\n')...)); err != nil {
		t.Fatalf("formatting compatibility changed: %v", err)
	}
	for _, bad := range [][]byte{
		[]byte(strings.Replace(string(raw), `"Version":`, `"Version":"`+Version+`","Version":`, 1)),
		[]byte(strings.Replace(string(raw), `"Members":[`, `"Members":null,"Extra":[`, 1)),
		[]byte(strings.Replace(string(raw), `"Key":"card"`, `"Key":null`, 1)),
		append(append([]byte{}, raw...), []byte(` {}`)...),
		[]byte(strings.Replace(string(raw), `"Version":"`+Version+`","Key":"card"`, `"Key":"card","Version":"`+Version+`"`, 1)),
	} {
		if _, err := ParseSpec(bad); !errors.Is(err, ErrBootstrap) {
			t.Fatalf("ambiguous specification accepted: %s: %v", bad, err)
		}
	}
}
