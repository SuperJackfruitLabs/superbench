//go:build linux

package serviceidentity

import (
	"os"
	"reflect"
)

func helperCredentials(uid, euid int) error {
	if uid != 0 || euid != 0 {
		return ErrAuthority
	}
	return nil
}

// ResolveHelperRegistry accepts no asserted identity or policy. It reads only
// the fixed public registry, under the actual dedicated helper's root identity.
// It never returns daemon Authority or grants execution permission.
func ResolveHelperRegistry() (HelperRegistry, error) {
	if err := helperCredentials(os.Getuid(), os.Geteuid()); err != nil {
		return HelperRegistry{}, err
	}
	registry, proof, err := readInstalled()
	if err != nil {
		return HelperRegistry{}, err
	}
	return HelperRegistry{registry: registry, physical: proof}, nil
}

func (h HelperRegistry) Recheck() error {
	if h.physical == nil {
		return ErrAuthority
	}
	if err := helperCredentials(os.Getuid(), os.Geteuid()); err != nil {
		return err
	}
	registry, proof, err := readInstalled()
	if err != nil {
		return err
	}
	if registry != h.registry || !reflect.DeepEqual(proof, h.physical) {
		return ErrAuthority
	}
	return nil
}
