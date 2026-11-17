// Package payloadinput is the fixed helper's finite stdin leaf: copied opaque
// bytes in an anonymous, sealed, read-only memfd that is bound to stdin once,
// after the credential drop. It parses nothing and selects no authority.
package payloadinput

import "errors"

// ErrInput refuses input storage that is not exactly sealed, bounded and unread.
var ErrInput = errors.New("invalid finite payload input")
