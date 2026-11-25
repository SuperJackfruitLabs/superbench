//go:build !linux

package remotessh

import (
	"errors"
	"os"
)

// Remote sessions run only on Linux; elsewhere a PTY request is refused.
const ptySupported = false

func openPTY() (*os.File, *os.File, *os.File, error) {
	return nil, nil, nil, errors.New("pty unsupported")
}

func setWinsize(*os.File, uint32, uint32) error { return errors.New("pty unsupported") }
