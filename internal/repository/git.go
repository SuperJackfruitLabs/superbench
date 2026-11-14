package repository

import (
	"context"
	"fmt"
	"os"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
)

const outputLimit = 64 << 10

type bounded struct {
	data     []byte
	overflow bool
}

func (b *bounded) Write(p []byte) (int, error) {
	n := len(p)
	keep := min(n, outputLimit-len(b.data))
	b.data = append(b.data, p[:keep]...)
	b.overflow = b.overflow || keep != n
	return n, nil
}
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := gitcontrol.Command(ctx, dir, args...)
	cmd.WaitDelay = 2 * time.Second
	var out, diagnostic bounded
	cmd.Stdout = &out
	cmd.Stderr = &diagnostic
	cmd.Env = environment(os.Environ())
	e := gitcontrol.Run(ctx, cmd)
	if ctx.Err() != nil {
		return "", fmt.Errorf("mirror git %s interrupted: %w", args[0], ctx.Err())
	}
	if e != nil {
		return "", fmt.Errorf("mirror git %s failed: %w", args[0], e)
	}
	if out.overflow {
		return "", fmt.Errorf("mirror git %s output exceeds 64 KiB", args[0])
	}
	return string(out.data), nil
}
func environment(env []string) []string {
	return gitcontrol.Environment(env)
}
