// Package gitpolicy defines the fixed registered-checkout Git argument policy.
// It grants no execution, filesystem or lease authority.
package gitpolicy

import (
	"errors"
	"slices"
	"strings"
)

var ErrPolicy = errors.New("managed Git policy refused")

// Arguments returns owned argv. The managed helper always uses the pinned cwd.
func Arguments(args ...string) []string {
	prefix := []string{"git", "--no-optional-locks", "--no-replace-objects",
		"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "push.followTags=false",
		"-c", "diff.external=", "-c", "core.sshCommand=/usr/bin/ssh -F /dev/null",
		"-c", "credential.helper=", "-c", "core.alternateRefsCommand=", "-c", "protocol.ext.allow=never",
		"-C", "."}
	if len(args) != 0 && diffCommand(args[0]) {
		prefix = append(prefix, args[0], "--no-ext-diff", "--no-textconv")
		return append(prefix, args[1:]...)
	}
	return append(prefix, args...)
}

func Subcommand(args []string) bool {
	if len(args) == 0 || args[0] == "" || strings.HasPrefix(args[0], "-") {
		return false
	}
	for _, arg := range args[1:] {
		if arg == "--ext-diff" || arg == "--textconv" {
			return false
		}
	}
	return true
}

// Validate refuses missing/overridden prefix and global options after it.
func Validate(argv []string) error {
	prefix := Arguments()
	if len(argv) <= len(prefix) || !slices.Equal(argv[:len(prefix)], prefix) || !Subcommand(argv[len(prefix):]) {
		return ErrPolicy
	}
	if diffCommand(argv[len(prefix)]) {
		rest := argv[len(prefix)+1:]
		if len(rest) < 2 || rest[0] != "--no-ext-diff" || rest[1] != "--no-textconv" {
			return ErrPolicy
		}
	}
	return nil
}

func diffCommand(command string) bool {
	switch command {
	case "diff", "diff-tree", "diff-index", "diff-files", "show", "log", "format-patch":
		return true
	}
	return false
}
