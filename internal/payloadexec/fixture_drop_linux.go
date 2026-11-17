//go:build linux && freshstoragefixture

package payloadexec

// FixtureDrop is the production credential drop and payload confinement. It
// exists only under the privileged storage fixture's build tag, which runs it
// in a re-executed root child before trying the owner's quota-escape routes.
func FixtureDrop(p Policy) error { return dropCredentials(p) }
