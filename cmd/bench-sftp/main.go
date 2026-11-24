// bench-sftp serves SFTP on stdin and stdout, confined to its working
// directory. bench-payload starts it after the drop to the payload account,
// inside the session's namespace; it takes no arguments and refuses root.
package main

import (
	"fmt"
	"io"
	"os"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotesftp"
)

func main() {
	os.Exit(run(os.Args, os.Getuid(), os.Geteuid(), os.Stdin, os.Stdout, os.Stderr))
}

type stdio struct {
	io.Reader
	io.WriteCloser
}

func run(args []string, uid, euid int, stdin io.Reader, stdout io.WriteCloser, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "bench-sftp: takes no arguments")
		return 2
	}
	for _, id := range []int{uid, euid} {
		if err := remotesftp.RefuseRoot(id); err != nil {
			fmt.Fprintln(stderr, "bench-sftp:", err)
			return 1
		}
	}
	root, err := os.OpenRoot(".")
	if err != nil {
		fmt.Fprintln(stderr, "bench-sftp:", err)
		return 1
	}
	defer root.Close()
	if err := remotesftp.Serve(stdio{stdin, stdout}, root); err != nil {
		fmt.Fprintln(stderr, "bench-sftp:", err)
		return 1
	}
	return 0
}
