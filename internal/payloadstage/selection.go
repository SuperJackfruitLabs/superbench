package payloadstage

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// Arguments validates the existing payload argv selection. It grants no
// layout, writer or installed service authority.
func Arguments(args []string) error {
	if len(args) == 0 || len(args) > 128 || args[0] == "" {
		return errors.New("workspace payload requires 1-128 arguments and a nonempty executable")
	}
	for _, arg := range args {
		if len(arg) > 8192 || !utf8.ValidString(arg) || strings.ContainsRune(arg, 0) {
			return errors.New("invalid workspace payload argument")
		}
	}
	return nil
}
