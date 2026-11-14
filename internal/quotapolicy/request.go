// Package quotapolicy parses repository requests without granting host authority.
package quotapolicy

import (
	"errors"
	"regexp"
	"strconv"
)

type Request struct {
	Size *string `toml:"size"`
}

var quantity = regexp.MustCompile(`^([1-9][0-9]*)(B|K|KiB|M|MiB|G|GiB|T|TiB|P|PiB)?$`)

func (r Request) Bytes() (uint64, error) {
	if r.Size == nil {
		return 0, nil
	}
	match := quantity.FindStringSubmatch(*r.Size)
	invalid := errors.New("quota size must be positive whole KiB, at most 1 PiB, using B/K/M/G/T/P or binary units")
	if match == nil {
		return 0, invalid
	}
	n, e := strconv.ParseUint(match[1], 10, 64)
	if e != nil {
		return 0, invalid
	}
	shift := map[string]uint{"": 0, "B": 0, "K": 10, "KiB": 10, "M": 20, "MiB": 20, "G": 30, "GiB": 30, "T": 40, "TiB": 40, "P": 50, "PiB": 50}[match[2]]
	if n > (1<<50)>>shift {
		return 0, invalid
	}
	n <<= shift
	if n < 1024 || n%1024 != 0 {
		return 0, invalid
	}
	return n, nil
}

func ValidBytes(n uint64) bool { return n == 0 || n >= 1024 && n%1024 == 0 && n <= 1<<50 }
