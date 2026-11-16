//go:build !linux

package main

import (
	"errors"
	"io"
	"os"
)

func runBootstrapCommand([]string, bool, *os.File, io.Writer) error {
	return errors.New("fresh payload bootstrap requires Linux")
}
