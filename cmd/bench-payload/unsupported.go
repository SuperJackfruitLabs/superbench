//go:build !linux || cgo

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "managed payload refused: unsupported")
	os.Exit(1)
}
