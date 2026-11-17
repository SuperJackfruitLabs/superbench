package scalarapi

import "testing"

func TestRepositoryDisplayRedactsSecrets(t *testing.T) {
	got := DisplayRepository("https://user:password@example.invalid/repo?access_token=secret#private")
	if got != "https://user@example.invalid/repo" {
		t.Fatalf("repository not sanitized: %s", got)
	}
}
