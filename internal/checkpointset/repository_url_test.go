package checkpointset

import (
	"encoding/json"
	"testing"
)

func TestRepositoryURLAuthorityMatchesCanonicalValidation(t *testing.T) {
	for _, identity := range []string{
		"https://example.invalid/repo.git",
		"HTTP://example.invalid/repo.git",
		"SSH://workspace@example.invalid:22/repo.git",
		"file://LOCALHOST/tmp/repo.git",
		"FILE://LOCALHOST/tmp/repo.git",
		"file://localhost",
		"FILE://LOCALHOST",
		"file://",
		"FILE://",
		"https://example.invalid:0/repo.git",
		"https://example.invalid:65535/repo.git",
		"https://example.invalid:00065535/repo.git",
		"ssh://workspace@example.invalid:65535/repo.git",
		"https://[::1]:65535/repo.git",
		"ssh://workspace@[2001:db8::1]:22/repo.git",
	} {
		t.Run(identity, func(t *testing.T) {
			manifest := fixture()
			manifest.Members[0].Repository = identity
			raw, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := Parse(raw)
			if err != nil {
				t.Fatal("canonical URL identity rejected", err)
			}
			if parsed.Members[0].Repository != identity {
				t.Fatal("validation normalized literal repository identity")
			}
		})
	}
	for _, identity := range []string{
		"https://example.invalid:65536/repo.git",
		"https://example.invalid:99999/repo.git",
		"https://example.invalid:18446744073709551616/repo.git",
		"ssh://workspace@example.invalid:65536/repo.git",
		"ssh://workspace@example.invalid:99999/repo.git",
		"file://LOCALHOST:22/tmp/repo.git",
		"file://@LOCALHOST/tmp/repo.git",
		"file://remote/tmp/repo.git",
		"HTTPS://user@example.invalid/repo.git",
		"SSH://workspace:password@example.invalid/repo.git",
		"https://[foo]/repo.git",
		"ssh://[not-ip]/repo.git",
		"https://[127.0.0.1]/repo.git",
		"https://[fe80::1%25eth0]/repo.git",
		"ssh://[fe80::1%25eth0]/repo.git",
	} {
		t.Run(identity, func(t *testing.T) {
			manifest := fixture()
			manifest.Members[0].Repository = identity
			if manifest.Validate() == nil {
				t.Fatal("invalid URL authority accepted")
			}
		})
	}
}
