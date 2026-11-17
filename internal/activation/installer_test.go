package activation

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var switchNames = []string{"fresh_payload", "finite_io", "receipt_lookup", "cancellation", "registration_observation"}

// The installer's default document must parse to the exact defaults and name
// every switch this package knows.
func TestInstallerWritesEveryKnownSwitchOff(t *testing.T) {
	installer, err := filepath.Abs(filepath.Join("..", "..", "deploy", "linux"))
	if err != nil {
		t.Fatal(err)
	}
	script := `import sys
sys.path.insert(0, sys.argv[1])
import install
sys.stdout.buffer.write(install.activation_document()[install.ACTIVATION_FILE][0])`
	out, err := exec.Command("python3", "-I", "-c", script, installer).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Parse(out); err != nil || got != (Switches{}) {
		t.Fatal(got, err)
	}
	var all Switches
	for _, name := range switchNames {
		if !strings.Contains(string(out), `"`+name+`": false`) {
			t.Errorf("installer omits %s", name)
		}
		*all.field(name) = true
	}
	if all != (Switches{true, true, true, true, true}) {
		t.Fatal("a switch has no key", all)
	}
}
