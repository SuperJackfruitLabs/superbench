//go:build !linux

package serviceidentity

func ResolveHelperRegistry() (HelperRegistry, error) { return HelperRegistry{}, ErrAuthority }
func (h HelperRegistry) Recheck() error              { return ErrAuthority }
