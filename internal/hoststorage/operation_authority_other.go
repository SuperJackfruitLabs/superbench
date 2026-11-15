//go:build !linux

package hoststorage

import (
	"context"
	"errors"
)

func (Policy) OperationAuthority(context.Context) (string, error) {
	return "", errors.New("managed operation authority requires Linux XFS")
}
