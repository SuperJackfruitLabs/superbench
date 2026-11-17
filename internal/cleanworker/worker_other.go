//go:build !linux

package cleanworker

import (
	"fmt"
	"io"
)

// Main refuses: the worker's self-checks need Linux procfs.
func Main(_ []string, _ io.Reader, _, stderr io.Writer) int {
	fmt.Fprintln(stderr, "bench-quota clean-worker: requires Linux")
	return 1
}
