package output

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBoundedLogDrainsOutputAndMarksTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	capture, e := Open(path, 1024)
	if e != nil {
		t.Fatal(e)
	}
	defer capture.Close()
	cmd := exec.Command("sh", "-c", "head -c 131072 /dev/zero")
	cmd.Stdout = capture.Writer
	cmd.Stderr = capture.Writer
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	capture.Writer.Close()
	if e = cmd.Wait(); e != nil {
		t.Fatal(e)
	}
	if e = capture.Finish(); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if len(raw) != 1024 || !bytes.HasSuffix(raw, []byte(Marker)) || !capture.Truncated {
		t.Fatal("output not bounded/marked", len(raw))
	}
}
