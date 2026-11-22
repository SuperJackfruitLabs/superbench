package bench

import (
	"fmt"
	"strings"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func (s Store) git(dir string, args ...string) (string, error) {
	ctx := s.controlContext()
	cmd := gitcontrol.Command(ctx, dir, args...)
	var output strings.Builder
	cmd.Stdout, cmd.Stderr = &output, &output
	err := gitcontrol.Run(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w", args[0], err)
	}
	return strings.TrimSpace(output.String()), nil
}
func (s Store) clean(b *Bench) error {
	out, err := s.git(b.Path, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return err
	}
	if out != "" {
		return model.DirtyStatus("", out)
	}
	return nil
}
func (s Store) head(b *Bench) (string, error) { return s.git(b.Path, "rev-parse", "HEAD") }
func (s Store) confirmed(b *Bench, sha string) error {
	out, err := s.git(b.Path, "ls-remote", "--exit-code", "origin", "refs/heads/"+b.Branch)
	if err != nil {
		return err
	}
	fields := strings.Fields(out)
	if len(fields) != 2 || fields[0] != sha || fields[1] != "refs/heads/"+b.Branch {
		return fmt.Errorf("remote branch does not match checkpoint %s", sha)
	}
	return nil
}
