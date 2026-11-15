//go:build !linux && !darwin

package hostidentity

import (
	"context"
	"errors"
)

func Current(context.Context) (string, error) {
	return "", errors.New("stable host identity is unsupported on this platform")
}
