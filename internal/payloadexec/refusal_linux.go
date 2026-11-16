//go:build linux

package payloadexec

import "errors"

// refusalError preserves the original sentinel for internal errors.Is checks.
// Only a closed static phase crosses the dedicated helper's stderr boundary.
type refusalError struct {
	phase string
	cause error
}

func (e *refusalError) Error() string { return "managed payload refused: " + RefusalCode(e) }
func (e *refusalError) Unwrap() error { return e.cause }

// RefusalCode never returns an error message, caller input, path or environment.
// Unknown or untagged errors deliberately remain the static internal category.
func RefusalCode(err error) string {
	var refusal *refusalError
	if errors.As(err, &refusal) && refusal != nil {
		switch refusal.phase {
		case "caller", "policy", "stdio", "descriptors", "frame", "scope", "layout", "boundary", "environment", "executable", "credentials", "exec":
			return refusal.phase
		}
	}
	return "internal"
}
