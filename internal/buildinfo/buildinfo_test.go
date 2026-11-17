package buildinfo

import "testing"

func TestDefaultVersionIsDev(t *testing.T) {
	if Version != "dev" {
		t.Fatalf("an unstamped build must report dev, got %q", Version)
	}
}
