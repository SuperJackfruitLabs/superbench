// Package buildinfo carries the version a release build stamps in with
// -ldflags -X. A build that is not a release reports "dev".
package buildinfo

// Version is the release version without the leading v, set at link time by
// scripts/release.sh. It must stay a var: -X is silently ignored on a const.
var Version = "dev"
