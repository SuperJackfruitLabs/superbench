//go:build !linux

package bench

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestUnsupportedFencedRunDoesNotAcquireLease(t *testing.T) {
	root := t.TempDir()
	s := Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data")}
	if _, e := s.Run(context.Background(), "task", "writer", []string{"sh", "-c", "exit 0"}, true); e == nil {
		t.Fatal("unsupported fenced execution accepted")
	}
	if _, e := os.Stat(s.State); !os.IsNotExist(e) {
		t.Fatal("unsupported execution created state or lease")
	}
}
