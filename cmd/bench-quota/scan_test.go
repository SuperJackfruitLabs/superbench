package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestScanProjectsRequiresDirectRoot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("ordinary tests run unprivileged")
	}
	var out bytes.Buffer
	if err := run([]string{"scan-projects"}); err == nil || !strings.Contains(err.Error(), "direct root") {
		t.Fatalf("unprivileged scan: %v", err)
	}
	if err := scanProjects("/etc/superbench/storage.json", &out); err == nil || out.Len() != 0 {
		t.Fatalf("unprivileged scan wrote %q: %v", out.String(), err)
	}
}
