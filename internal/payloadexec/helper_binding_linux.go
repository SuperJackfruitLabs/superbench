//go:build linux

package payloadexec

import (
	"bytes"
	"errors"
	"os"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/serviceidentity"
)

// helperBinding is private to the fixed helper. No caller-supplied Policy or
// boolean can construct it through a public API or bypass Exec's checks.
type helperBinding struct {
	policy   Policy
	registry serviceidentity.HelperRegistry
}

func matchHelperPolicy(private, public Policy) error {
	left, leftErr := private.Bytes()
	right, rightErr := public.Bytes()
	if leftErr != nil || rightErr != nil || !bytes.Equal(left, right) {
		return errors.Join(ErrBoundary, serviceidentity.ErrAuthority, leftErr, rightErr)
	}
	return nil
}

func loadHelperBinding() (helperBinding, error) {
	if os.Getuid() != 0 || os.Geteuid() != 0 {
		return helperBinding{}, ErrBoundary
	}
	private, err := LoadFixedPolicy()
	if err != nil {
		return helperBinding{}, errors.Join(ErrBoundary, err)
	}
	registry, err := serviceidentity.ResolveHelperRegistry()
	if err != nil {
		return helperBinding{}, errors.Join(ErrBoundary, err)
	}
	public, err := registry.Policy()
	if err != nil {
		return helperBinding{}, errors.Join(ErrBoundary, err)
	}
	if err := matchHelperPolicy(private, public); err != nil {
		return helperBinding{}, err
	}
	return helperBinding{policy: private, registry: registry}, nil
}

func (b helperBinding) recheck() error {
	if os.Getuid() != 0 || os.Geteuid() != 0 {
		return ErrBoundary
	}
	// Always reopen the fixed root-private file. A saved Policy value alone
	// cannot establish that the installed helper still uses the same authority.
	current, err := LoadFixedPolicy()
	if err != nil {
		return errors.Join(ErrBoundary, err)
	}
	if current != b.policy {
		return errors.Join(ErrBoundary, serviceidentity.ErrAuthority)
	}
	public, err := b.registry.Policy() // Rechecks the ORIGINAL registry inode/chain.
	if err != nil {
		return errors.Join(ErrBoundary, err)
	}
	return matchHelperPolicy(current, public)
}
