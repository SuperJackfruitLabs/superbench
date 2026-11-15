//go:build linux

package execution

// CurrentIdentity reads the same host/boot authority used by Create and Fence,
// without creating a scope. Configuration alone cannot supply this identity.
func CurrentIdentity() (Identity, error) { return bootIdentity() }
