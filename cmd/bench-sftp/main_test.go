package main

import (
	"bytes"
	"testing"
)

func TestArgumentsExitTwo(t *testing.T) {
	var stderr bytes.Buffer
	if code := run([]string{"bench-sftp", "-h"}, 1000, 1000, nil, nil, &stderr); code != 2 {
		t.Fatalf("run with an argument = %d, want 2", code)
	}
}

func TestRootRefused(t *testing.T) {
	for _, ids := range [][2]int{{0, 0}, {1000, 0}, {0, 1000}} {
		var stderr bytes.Buffer
		if code := run([]string{"bench-sftp"}, ids[0], ids[1], nil, nil, &stderr); code != 1 || stderr.Len() == 0 {
			t.Fatalf("uid %d euid %d: exit %d, %q; want 1 with a reason", ids[0], ids[1], code, stderr.String())
		}
	}
}
