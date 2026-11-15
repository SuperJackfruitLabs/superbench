package localprep

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestGitIdentityOutputDrainsWithinFixedMemory(t *testing.T) {
	var output identityOutput
	n, e := io.Copy(&output, strings.NewReader(strings.Repeat("x", 1<<20)))
	if e != nil || n != 1<<20 || len(output.data) != 16<<10 || cap(output.data) > 16<<10 || !output.overflow {
		t.Fatalf("unbounded or undrained Git observation: bytes=%d retained=%d capacity=%d overflow=%v error=%v", n, len(output.data), cap(output.data), output.overflow, e)
	}
}

func TestLocalOutputTruncationIsExplicitAndBounded(t *testing.T) {
	file, e := os.CreateTemp(t.TempDir(), "log")
	if e != nil {
		t.Fatal(e)
	}
	defer file.Close()
	output := boundedWriter{file: file, left: 1024}
	if n, e := output.Write([]byte(strings.Repeat("x", 4096))); e != nil || n != 4096 {
		t.Fatal(n, e)
	}
	info, e := file.Stat()
	if e != nil || info.Size() != 1024 || !output.truncated {
		t.Fatal("local output bound missing", e)
	}
}
