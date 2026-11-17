package registeredinspection

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func absentInputs() Inputs {
	return Inputs{Version: InputsVersion, Member: "web", Config: InputFile{Name: ".bench/config.toml"}, Manifests: []InputFile{{Name: "package.json"}, {Name: "pnpm-lock.yaml"}, {Name: "package-lock.json"}}}
}
func inputHash(raw []byte) string { s := sha256.Sum256(raw); return hex.EncodeToString(s[:]) }
func TestInputsCanonicalAndExistingPreparationDigest(t *testing.T) {
	i := absentInputs()
	raw, err := i.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseInputs(raw)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := got.PreparationDigest("")
	if err != nil {
		t.Fatal(err)
	}
	expected := inputHash([]byte("\x00package.json\x00absent\x00pnpm-lock.yaml\x00absent\x00package-lock.json\x00absent"))
	if digest != expected {
		t.Fatalf("absent digest %s != %s", digest, expected)
	}
	config := []byte("version = 1\n")
	i.Config = InputFile{Name: ".bench/config.toml", Present: true, Bytes: uint64(len(config)), SHA256: inputHash(config), Data: config}
	dependency := []byte(`{"name":"example"}`)
	i.Manifests[0] = InputFile{Name: "package.json", Present: true, Bytes: uint64(len(dependency)), SHA256: inputHash(dependency)}
	digest, err = i.PreparationDigest(i.Config.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	dependencyHash := sha256.Sum256(dependency)
	expected = inputHash(append(append([]byte(i.Config.SHA256+"\x00package.json\x00"), dependencyHash[:]...), []byte("\x00pnpm-lock.yaml\x00absent\x00package-lock.json\x00absent")...))
	if digest != expected {
		t.Fatalf("present digest %s != %s", digest, expected)
	}
	if _, err = i.PreparationDigest(strings.Repeat("0", 64)); !errors.Is(err, ErrBoundary) {
		t.Fatalf("foreign config digest: %v", err)
	}
	raw, err = i.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{bytes.Replace(raw, []byte(`"Member":"web"`), []byte(`"Member":"web","Member":"web"`), 1), bytes.Replace(raw, []byte(`"Member":"web"`), []byte(`"member":"web"`), 1), append([]byte{0xef, 0xbb, 0xbf}, raw...), bytes.Replace(raw, []byte(`"Manifests":`), []byte(`"Unknown":false,"Manifests":`), 1)} {
		if _, err := ParseInputs(bad); !errors.Is(err, ErrBoundary) {
			t.Fatalf("accepted invalid encoding: %q: %v", bad, err)
		}
	}
}
func TestInputsEmptyPresentAndBounds(t *testing.T) {
	i := absentInputs()
	absent, _ := i.PreparationDigest("")
	i.Config = InputFile{Name: ".bench/config.toml", Present: true, SHA256: inputHash(nil)}
	present, err := i.PreparationDigest(i.Config.SHA256)
	if err != nil || present == absent {
		t.Fatalf("empty present conflated with absence: %s %v", present, err)
	}
	i.Config.Data = bytes.Repeat([]byte{'x'}, MaxConfigBytes)
	i.Config.Bytes = MaxConfigBytes
	i.Config.SHA256 = inputHash(i.Config.Data)
	raw, err := i.Bytes()
	if err != nil || len(raw) > MaxInputs {
		t.Fatalf("maximum config: %d %v", len(raw), err)
	}
	i.Config.Data = append(i.Config.Data, 'x')
	i.Config.Bytes++
	i.Config.SHA256 = inputHash(i.Config.Data)
	if _, err := i.Bytes(); !errors.Is(err, ErrBoundary) {
		t.Fatalf("oversized config: %v", err)
	}
	i = absentInputs()
	i.Manifests[0].Name = "../package.json"
	if err := i.Validate(); !errors.Is(err, ErrBoundary) {
		t.Fatalf("foreign input path: %v", err)
	}
}
