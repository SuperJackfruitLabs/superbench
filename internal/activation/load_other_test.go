//go:build !linux

package activation

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Off Linux nothing can check the installed ownership, so any present file,
// even a valid one, refuses; only an absent file keeps the defaults.
func TestLoadOffLinuxRefusesAPresentFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "activation.json")
	if got, err := loadAt(path); err != nil || got != (Switches{}) {
		t.Fatal("absent file changed the defaults", got, err)
	}
	if err := os.WriteFile(path, []byte(`{"version":"superbench.activation.v1","cancellation":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	if got, err := loadAt(path); !errors.Is(err, ErrActivation) || got != (Switches{}) {
		t.Fatal("present file accepted off Linux", got, err)
	}
}
