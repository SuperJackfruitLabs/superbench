//go:build darwin

package hostidentity

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

// boundedOutput drains the command while retaining at most the observation
// limit. Neither a platform identifier nor raw hardware output enters errors.
type boundedOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (64 << 10) - b.Len()
	if n > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

func Current(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/sbin/ioreg", "-rd1", "-c", "IOPlatformExpertDevice")
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LC_ALL=C"}
	var out boundedOutput
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", errors.Join(errors.New("platform identity inspection failed"), ctx.Err())
	}
	if out.overflow {
		return "", errors.New("platform identity observation exceeds its bound")
	}
	return darwinIdentity(out.Bytes())
}
