package checkpointset

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type limitedOutput struct {
	data     []byte
	overflow bool
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	keep := min(n, MaxRaw+4096-len(b.data))
	b.data = append(b.data, p[:keep]...)
	b.overflow = b.overflow || keep != n
	return n, nil
}
func gitEnvironment() []string {
	var env []string
	for _, v := range os.Environ() {
		k, _, _ := strings.Cut(v, "=")
		if strings.HasPrefix(k, "GIT_") || k == "BENCH_LEASE_TOKEN" {
			continue
		}
		env = append(env, v)
	}
	return append(env, "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_AUTHOR_NAME=SuperBench", "GIT_AUTHOR_EMAIL=checkpoint@example.invalid", "GIT_COMMITTER_NAME=SuperBench", "GIT_COMMITTER_EMAIL=checkpoint@example.invalid")
}
func runGit(ctx context.Context, path string, input []byte, extraEnv []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-replace-objects", "-c", "remote.origin.mirror=false", "-c", "push.followTags=false", "-c", "commit.gpgSign=false", "-c", "i18n.commitEncoding=UTF-8", "-C", path}, args...)...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = append(gitEnvironment(), extraEnv...)
	cmd.Stdin = bytes.NewReader(input)
	var out limitedOutput
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("checkpoint git %s failed", args[0])
	}
	if out.overflow {
		return "", errors.New("checkpoint git output exceeds bounds")
	}
	return string(out.data), nil
}
func gitText(ctx context.Context, path string, args ...string) (string, error) {
	s, e := runGit(ctx, path, nil, nil, args...)
	return strings.TrimSpace(s), e
}
func remoteRef(ctx context.Context, path, ref string) (string, error) {
	out, e := gitText(ctx, path, "ls-remote", "--refs", "origin", ref)
	if e != nil {
		return "", e
	}
	if out == "" {
		return "", nil
	}
	f := strings.Fields(out)
	if len(f) != 2 || f[1] != ref || (!sha1Pattern.MatchString(f[0]) && !sha256Pattern.MatchString(f[0])) {
		return "", errors.New("invalid checkpoint remote ref")
	}
	return f[0], nil
}
func (c Coordinator) mirror(ctx context.Context, repo string) (string, error) {
	p, ok := c.Mirrors[repo]
	if !ok || p == "" {
		return "", errors.New("checkpoint repository is not configured")
	}
	bare, e := gitText(ctx, p, "rev-parse", "--is-bare-repository")
	if e != nil || bare != "true" {
		return "", errors.New("checkpoint mirror is not bare")
	}
	origin, e := gitText(ctx, p, "remote", "get-url", "origin")
	if e != nil || origin != repo {
		return "", errors.New("checkpoint mirror origin differs from configured identity")
	}
	pushURLs, e := gitText(ctx, p, "remote", "get-url", "--push", "--all", "origin")
	if e != nil || pushURLs != repo {
		return "", errors.New("checkpoint mirror push identity differs")
	}
	return p, nil
}
func confirm(ctx context.Context, path, ref, commit string) error {
	got, e := remoteRef(ctx, path, ref)
	if e != nil {
		return e
	}
	if got != commit {
		return errors.New("checkpoint remote confirmation differs")
	}
	return nil
}
func push(ctx context.Context, path, ref, commit string) error {
	if _, e := gitText(ctx, path, "push", "--no-verify", "origin", commit+":"+ref); e != nil {
		return e
	}
	return confirm(ctx, path, ref, commit)
}

// Immutable generation pins are lightweight tags. Ordinary Git push refuses
// replacing an existing tag even when the proposed commit is a descendant.
func createPin(ctx context.Context, path, ref, commit string) error {
	return push(ctx, path, ref, commit)
}
