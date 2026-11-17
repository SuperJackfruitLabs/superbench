//go:build !linux

package activation

import (
	"errors"
	"io/fs"
	"os"
)

// Load keeps the defaults when no activation file exists. The installed
// ownership checks are Linux-only, so a present file refuses elsewhere.
func Load() (Switches, error) {
	if _, err := os.Lstat(FixedPath); errors.Is(err, fs.ErrNotExist) {
		return Switches{}, nil
	}
	return Switches{}, refuse("the activation file requires the Linux installation")
}
