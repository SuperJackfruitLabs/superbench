//go:build linux

package payloadexec

import (
	"errors"
	"os"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadinput"
)

// openPayloadInput validates the framed request before allocating anything,
// then delegates to the sealed memfd leaf. Errors keep ErrFrame identity.
func openPayloadInput(r Request) (*os.File, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	f, err := payloadinput.Open(r.Input)
	if err != nil {
		return nil, errors.Join(ErrFrame, err)
	}
	return f, nil
}

// Called only after all privileged authority/stdio checks and credential drop.
func bindPayloadInput(f *os.File) error {
	if err := payloadinput.Bind(f); err != nil {
		return errors.Join(ErrFrame, err)
	}
	return nil
}
