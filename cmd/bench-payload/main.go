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
	// Exec and Session may have irreversibly dropped credentials before
	// returning. Never retry, restore privileges, print private inputs, or
	// continue this process. A socket on fd 0 selects session mode; no frame
	// kind falls back to the other. A session's PID 1 stage is this binary
	// re-executed by Session itself, never reachable through sudo.
	var err error
	if os.Getpid() == 1 {
		// PID 1 is only ever a session's PID 1 stage; SessionInit refuses
		// anything else. It never falls through to another mode.
		err = payloadexec.SessionInit()
	} else if payloadexec.SessionStdin() {
		err = payloadexec.Session()
	} else {
		err = payloadexec.Exec()
	}
	if err == nil {
		// Only read-only layout or preparation-input inspections return successfully.
		return
	}
	fmt.Fprintf(os.Stderr, "managed payload refused: %s\n", payloadexec.RefusalCode(err))
	os.Exit(1)
}
