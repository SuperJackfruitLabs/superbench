//go:build freshstoragefixture

package mountnsfixture

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrNamespace means the private mount namespace itself could not be made.
var ErrNamespace = errors.New("private mount namespace unavailable")

// ErrNotVisible means the namespace exists but holds no mount at the root.
var ErrNotVisible = errors.New("mount not visible in this mount namespace")

// ParseMountID returns the mount ID of the topmost mount at root in mountinfo
// text. Mount points are octal-escaped by the kernel.
func ParseMountID(mountinfo []byte, root string) (string, error) {
	id, mounts := "", 0
	for n, line := range strings.Split(strings.TrimRight(string(mountinfo), "\n"), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			return "", fmt.Errorf("mountinfo line %d is malformed", n+1)
		}
		mounts++
		point, err := unescape(fields[4])
		if err != nil {
			return "", fmt.Errorf("mountinfo line %d: %w", n+1, err)
		}
		if point == root {
			if _, err = strconv.ParseUint(fields[0], 10, 64); err != nil {
				return "", fmt.Errorf("mountinfo line %d has no numeric mount ID", n+1)
			}
			id = fields[0] // a later line is a mount stacked on top
		}
	}
	if id == "" {
		return "", fmt.Errorf("%w: %s (among %d mounts)", ErrNotVisible, root, mounts)
	}
	return id, nil
}

func unescape(field string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(field); i++ {
		if field[i] != '\\' {
			out.WriteByte(field[i])
			continue
		}
		if i+4 > len(field) {
			return "", errors.New("truncated octal escape")
		}
		v, err := strconv.ParseUint(field[i+1:i+4], 8, 8)
		if err != nil {
			return "", errors.New("invalid octal escape")
		}
		out.WriteByte(byte(v))
		i += 3
	}
	return out.String(), nil
}
