// Package childlog reads the log of a child test binary run with -test.v.
package childlog

import "regexp"

// skipLine is Go's own line for a skipped test or subtest under -test.v:
// "--- SKIP: Name (0.00s)", indented for a subtest. A marker or message that
// merely contains the word is not a skip.
var skipLine = regexp.MustCompile(`(?m)^[ \t]*--- SKIP: `)

// Skipped reports whether a child test log records any skipped test.
func Skipped(log []byte) bool { return skipLine.Match(log) }
