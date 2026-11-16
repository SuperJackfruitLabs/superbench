package gitcontrol

import (
	"context"
	"os/exec"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitpolicy"
)

// ManagedCommand carries the helper-enforced policy and descriptor-relative cwd.
// The physical directory is used only for the daemon's prepared command check;
// the helper enters its held descriptor and Git receives -C .
func ManagedCommand(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := Command(ctx, ".", args...)
	cmd.Args = gitpolicy.Arguments(args...)
	cmd.Dir = dir
	return cmd
}
