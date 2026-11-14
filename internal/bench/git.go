package bench

import (
	"fmt"
	"os/exec"
	"strings"
)

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
func clean(b *Bench) error {
	out, err := git(b.Path, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return err
	}
	if out != "" {
		return ErrDirty
	}
	return nil
}
func head(b *Bench) (string, error) { return git(b.Path, "rev-parse", "HEAD") }
func confirmed(b *Bench, sha string) error {
	out, err := git(b.Path, "ls-remote", "--exit-code", "origin", "refs/heads/"+b.Branch)
	if err != nil {
		return err
	}
	fields := strings.Fields(out)
	if len(fields) != 2 || fields[0] != sha || fields[1] != "refs/heads/"+b.Branch {
		return fmt.Errorf("remote branch does not match checkpoint %s", sha)
	}
	return nil
}
