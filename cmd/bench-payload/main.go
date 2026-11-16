//go:build linux && !cgo

// bench-payload is a dedicated fixed-policy privilege boundary, never a daemon.
package main

import (
	"fmt"
	"os"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
)

func main() {
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "managed payload refused: arguments")
		os.Exit(2)
	}
	// Exec may have irreversibly dropped credentials before returning. Never
	// retry, restore privileges, print private inputs, or continue this process.
	err := payloadexec.Exec()
	if err == nil {
		// Only the read-only inspection variant returns successfully.
		return
	}
	fmt.Fprintf(os.Stderr, "managed payload refused: %s\n", payloadexec.RefusalCode(err))
	os.Exit(1)
}
