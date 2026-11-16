//go:build linux

package payloadexec

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitpolicy"
)

// gitObservation is a fixed read-only builtin, invoked only after credential
// drop inside the already-pinned scope/cwd. It never receives Git's pack stdin.
// Configuration key names remain private; no config values enter diagnostics.
func gitObservation(environment []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	argv := gitpolicy.Arguments(args...)
	cmd := exec.CommandContext(ctx, "/usr/bin/git", argv[1:]...)
	cmd.Env = environment
	cmd.WaitDelay = 2 * time.Second
	out := &gitObservationBuffer{}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		return nil, errors.Join(ErrBoundary, err)
	}
	return bytes.Clone(out.Bytes()), nil
}

type gitObservationBuffer struct{ bytes.Buffer }

func (b *gitObservationBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 64<<10 {
		return 0, ErrBoundary
	}
	return b.Buffer.Write(p)
}

// verifyManagedGit refuses dynamically named filter/diff/alias programs rather
// than guessing driver names or rewriting the repository's configuration.
func verifyManagedGit(r Request, admin *os.File, environment []string) error {
	if gitpolicy.Validate(r.Arguments) != nil || admin == nil {
		return ErrBoundary
	}
	raw, err := gitObservation(environment, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return err
	}
	path := strings.TrimSuffix(string(raw), "\n")
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
		return ErrBoundary
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return errors.Join(ErrBoundary, err)
	}
	actual := os.NewFile(uintptr(fd), "observed Git admin")
	defer actual.Close()
	want, e := admin.Stat()
	got, other := actual.Stat()
	if e != nil || other != nil || !os.SameFile(want, got) {
		return errors.Join(ErrBoundary, e, other)
	}
	raw, err = gitObservation(environment, "config", "--includes", "--null", "--name-only", "--list")
	if err != nil {
		return err
	}
	for _, key := range strings.Split(string(raw), "\x00") {
		key = strings.ToLower(key)
		if strings.HasPrefix(key, "filter.") || strings.HasPrefix(key, "alias.") || strings.HasPrefix(key, "diff.") && (strings.HasSuffix(key, ".command") || strings.HasSuffix(key, ".textconv")) {
			return ErrBoundary
		}
	}
	return refuseGitAlternates(admin)
}

func refuseGitAlternates(admin *os.File) error {
	// Independent admins must not silently become linked worktrees.
	if err := absentGitChild(admin, "commondir"); err != nil {
		return err
	}
	objects, err := syscall.Openat(int(admin.Fd()), "objects", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return errors.Join(ErrBoundary, err)
	}
	defer syscall.Close(objects)
	info, err := syscall.Openat(objects, "info", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ENOENT) {
		return nil
	}
	if err != nil {
		return errors.Join(ErrBoundary, err)
	}
	f := os.NewFile(uintptr(info), "Git object info")
	defer f.Close()
	for _, name := range []string{"alternates", "http-alternates"} {
		if err := absentGitChild(f, name); err != nil {
			return err
		}
	}
	return nil
}
func absentGitChild(parent *os.File, name string) error {
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ENOENT) {
		return nil
	}
	if err == nil {
		syscall.Close(fd)
	}
	return ErrBoundary
}
