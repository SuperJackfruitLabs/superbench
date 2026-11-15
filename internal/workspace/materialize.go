package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/creation"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotapolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
)

// Materializer consumes the exact plan durably held by the caller. Gate must
// prove both that plan and the initialization lease under the caller's lock.
// Partial work remains available for retry; this component grants no readiness.
type Materializer struct {
	Git  func(context.Context, string, ...string) (string, error)
	Gate func(context.Context, Plan) error
	// Timeout bounds the complete operation, including every ownership gate and
	// Git callback. Zero uses repository.DefaultTimeout; at most one hour.
	Timeout time.Duration
}

func validateMaterialization(plan Plan) (string, error) {
	spec := Spec{Key: plan.Key, CheckpointRepository: plan.CheckpointRepository, QuotaBytes: plan.QuotaBytes}
	for _, member := range plan.Members {
		spec.Repositories = append(spec.Repositories, Repository{Name: member.Name, Repository: member.Repository, Branch: member.Branch, Base: member.Base})
	}
	normalized, err := normalize(spec)
	if err != nil {
		return "", err
	}
	data := filepath.Dir(filepath.Dir(plan.Root))
	if !filepath.IsAbs(plan.Root) || filepath.Clean(plan.Root) != plan.Root || plan.Root != filepath.Join(data, "benches", plan.Key) {
		return "", errors.New("workspace root differs from its registered location")
	}
	for i, member := range plan.Members {
		r := normalized.Repositories[i]
		if member.Branch != r.Branch || member.Base != r.Base || member.Path != filepath.Join(plan.Root, member.Name) || member.Mirror != repository.Path(data, member.Repository) || !model.ValidCommit(member.Source) || !quotapolicy.ValidBytes(member.RepositoryQuotaBytes) || member.RepositoryQuotaBytes != 0 && member.RepositoryQuotaBytes != plan.QuotaBytes {
			return "", errors.New("workspace member differs from the pinned normalized plan")
		}
		if member.ObjectFormat != "sha1" && member.ObjectFormat != "sha256" || member.ObjectFormat == "sha1" && len(member.Source) != 40 || member.ObjectFormat == "sha256" && len(member.Source) != 64 {
			return "", errors.New("invalid workspace member object format")
		}
		for _, previous := range plan.Members[:i] {
			if previous.Repository == member.Repository && branchNamespaceConflict(previous.Branch, member.Branch) {
				return "", errors.New("workspace task branches conflict in the repository ref namespace")
			}
		}
	}
	return data, nil
}

func branchNamespaceConflict(a, b string) bool {
	return strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// Ref ownership is checked without pruning stale registrations or moving any
// worktree. Even a same-commit branch elsewhere belongs to that checkout.
func validateWorktreeOwnership(raw string, member Member) error {
	for _, record := range strings.Split(raw, "\x00\x00") {
		if record == "" {
			continue
		}
		var path, branch string
		for _, field := range strings.Split(record, "\x00") {
			if strings.HasPrefix(field, "worktree ") {
				if path != "" {
					return errors.New("invalid workspace worktree registration")
				}
				path = strings.TrimPrefix(field, "worktree ")
			}
			if strings.HasPrefix(field, "branch ") {
				if branch != "" {
					return errors.New("invalid workspace worktree branch registration")
				}
				branch = strings.TrimPrefix(field, "branch ")
			}
		}
		if path == "" {
			return errors.New("missing workspace worktree registration path")
		}
		if branch != "refs/heads/"+member.Branch {
			continue
		}
		actual, err := filepath.EvalSymlinks(path)
		expected, expectedErr := filepath.EvalSymlinks(member.Path)
		if err != nil || expectedErr != nil || actual != expected {
			return errors.New("workspace task branch is registered to a different worktree; preserve and reconcile")
		}
	}
	return nil
}

// physicalDirectories permits the system's ancestor aliases (for example /tmp)
// but every existing component below the registered data directory is physical.
func physicalDirectory(path string, optional bool) error {
	info, err := os.Lstat(path)
	if optional && os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("workspace storage must be a physical directory")
	}
	return nil
}

func memberIntent(member Member) *creation.Intent {
	return &creation.Intent{Source: member.Source, Base: member.Base, Bench: model.Bench{Repo: member.Repository, Branch: member.Branch, Path: member.Path, Mirror: member.Mirror, CreatedFrom: member.Source}}
}

func (m Materializer) Materialize(ctx context.Context, plan Plan) error {
	timeout := m.Timeout
	if timeout == 0 {
		timeout = repository.DefaultTimeout
	}
	if timeout < 0 || timeout > time.Hour {
		return errors.New("invalid workspace materialization timeout")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	data, err := validateMaterialization(plan)
	if err != nil {
		return err
	}
	if m.Git == nil || m.Gate == nil {
		return errors.New("workspace materialization requires managed Git and a durable ownership gate")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.Gate(ctx, plan); err != nil {
		return err
	}
	for _, path := range []string{data, filepath.Join(data, "mirrors")} {
		if err := physicalDirectory(path, false); err != nil {
			return err
		}
	}
	for _, path := range []string{filepath.Join(data, "benches"), plan.Root} {
		if err := physicalDirectory(path, true); err != nil {
			return err
		}
	}
	names := make(map[string]bool, len(plan.Members))
	for _, member := range plan.Members {
		names[member.Name] = true
	}
	entries, err := os.ReadDir(plan.Root)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		if !names[entry.Name()] || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return errors.New("workspace root contains an unfamiliar entry; preserve and reconcile")
		}
	}
	git := func(dir string, args ...string) (string, error) {
		out, err := m.Git(ctx, dir, args...)
		return strings.TrimSpace(out), err
	}
	files := creation.Files{Data: data, Git: git}
	// Validate the whole vector before creating the first member. No network
	// refresh occurs here: moving remote tips cannot change the selected sources.
	for _, member := range plan.Members {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := physicalDirectory(member.Mirror, false); err != nil {
			return err
		}
		if err := physicalDirectory(member.Path, true); err != nil {
			return err
		}
		bare, err := git(member.Mirror, "rev-parse", "--is-bare-repository")
		if err != nil {
			return err
		}
		if bare != "true" {
			return errors.New("workspace mirror is not bare")
		}
		origin, err := git(member.Mirror, "config", "--null", "--get-all", "remote.origin.url")
		if err != nil {
			return err
		}
		if origin != member.Repository+"\x00" {
			return errors.New("workspace mirror origin changed")
		}
		format, err := git(member.Mirror, "rev-parse", "--show-object-format=storage")
		if err != nil {
			return err
		}
		source, err := git(member.Mirror, "rev-parse", "--verify", member.Source+"^{commit}")
		if err != nil {
			return err
		}
		if source != member.Source || format != member.ObjectFormat {
			return errors.New("workspace source object changed")
		}
		ref := "refs/heads/" + member.Branch
		refs, err := git(member.Mirror, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/")
		if err != nil {
			return err
		}
		for _, line := range strings.Split(refs, "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[0] == ref && fields[1] != member.Source {
				return errors.New("workspace task branch differs from pinned source")
			}
			if len(fields) == 2 && branchNamespaceConflict(fields[0], ref) {
				return errors.New("workspace task branch conflicts with an existing local ref")
			}
		}
		worktrees, err := git(member.Mirror, "worktree", "list", "--porcelain", "-z")
		if err != nil {
			return err
		}
		if err := validateWorktreeOwnership(worktrees, member); err != nil {
			return err
		}
		if _, err := os.Lstat(member.Path); err == nil {
			if _, err := files.Finish(memberIntent(member)); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	for _, member := range plan.Members {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := m.Gate(ctx, plan); err != nil {
			return err
		}
		if _, err := files.Finish(memberIntent(member)); err != nil {
			return err
		}
	}
	return nil
}
