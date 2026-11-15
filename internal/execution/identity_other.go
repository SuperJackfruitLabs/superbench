//go:build !linux

package execution

func CurrentIdentity() (Identity, error) { return Identity{}, errNoFencing }
