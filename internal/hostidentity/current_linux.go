//go:build linux

package hostidentity

import (
	"context"
	"io"
	"os"
)

func Current(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f, err := os.Open("/etc/machine-id")
	if err != nil {
		return "", err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 129))
	if err != nil {
		return "", err
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	return linuxIdentity(raw)
}
