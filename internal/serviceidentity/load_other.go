//go:build !linux

package serviceidentity

type snapshot struct{}

// ResolveService requires the Linux installed identity boundary.
func ResolveService() (Authority, error) { return Authority{}, ErrAuthority }

func (a Authority) Recheck() error { return ErrAuthority }
