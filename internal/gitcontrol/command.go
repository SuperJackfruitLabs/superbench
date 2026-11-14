// Package gitcontrol builds host-control Git commands. Repository hooks and
// filesystem-monitor helpers are developer work and must run in a separately
// fenced payload, not after its writer has already been stopped.
package gitcontrol

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Command sets policy for this process only; it never changes repository or
// host configuration. Callers retain responsibility for bounding the context,
// validating arguments and containing any other Git transport/helpers.
func Command(ctx context.Context, dir string, args ...string) *exec.Cmd {
	policy := []string{"--no-replace-objects", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "push.followTags=false", "-C", dir}
	cmd := exec.CommandContext(ctx, "git", append(policy, args...)...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = Environment(os.Environ())
	return cmd
}

// Environment removes ambient Git command/configuration and path overrides.
// Explicit command-specific environment, such as a sealed commit's fixed author
// and timestamp, may be appended by a trusted caller after this filtering.
func Environment(env []string) []string {
	clean := make([]string, 0, len(env)+1)
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if key == "BENCH_LEASE_TOKEN" || strings.HasPrefix(key, "GIT_") {
			continue
		}
		clean = append(clean, entry)
	}
	return append(clean, "GIT_TERMINAL_PROMPT=0")
}
