// Package retainedfile publishes a root-owned approval file exclusively and
// atomically: a reader sees no file or the complete bytes, never a prefix.
// The implementation is Linux-only; other platforms build an empty package.
package retainedfile
