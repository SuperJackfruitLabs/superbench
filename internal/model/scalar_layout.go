package model

import (
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
	"path/filepath"
)

var ErrScalarLayout = errors.New("invalid registered scalar allocation layout")

// ValidateScalarLayout checks durable logical identity, not filesystem custody.
// Only fresh bootstrap may introduce this marker. Legacy records keep both
// fields empty; migration must never adopt their existing directories.
func ValidateScalarLayout(b *Bench) error {
	if b.PayloadLayoutVersion == "" && b.AllocationRoot == "" {
		return nil
	}
	root := b.AllocationRoot
	if b.Workspace != nil || b.PayloadLayoutVersion != payloadpolicy.LayoutVersion || len(b.RegistrationID) != 32 || !filepath.IsAbs(root) || filepath.Clean(root) != root || filepath.Base(root) != b.Key || filepath.Base(filepath.Dir(root)) != "benches" || b.Key == "." || b.Key == ".." || b.Key == "" || b.Path != filepath.Join(root, "worktree") {
		return ErrScalarLayout
	}
	return nil
}
