package repository

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
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
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.WaitDelay = 2 * time.Second
	var out, diagnostic bounded
	cmd.Stdout = &out
	cmd.Stderr = &diagnostic
	cmd.Env = environment(os.Environ())
	e := cmd.Run()
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
	var out []string
	for _, value := range env {
		key, _, _ := strings.Cut(value, "=")
		switch key {
		case "BENCH_LEASE_TOKEN", "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_SHALLOW_FILE", "GIT_NAMESPACE", "GIT_CONFIG", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS", "GIT_TERMINAL_PROMPT":
			continue
		}
		if strings.HasPrefix(key, "GIT_CONFIG_KEY_") || strings.HasPrefix(key, "GIT_CONFIG_VALUE_") {
			continue
		}
		out = append(out, value)
	}
	return append(out, "GIT_TERMINAL_PROMPT=0")
}
