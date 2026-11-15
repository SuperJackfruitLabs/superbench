package workspaceplan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
)

// DirectoryObservation binds a named directory or exact absence beneath the
// already observed parent. It never grants creation, deletion or adoption.
type DirectoryObservation struct {
	Path          string
	Present       bool
	Device, Inode uint64
	Mode          uint32
}

type MemberObservation struct {
	Name, Disposition                                  string // absent, empty, or checkout
	Directory, Mirror, Administration                  DirectoryObservation
	Head, TaskCommit, HeadsSHA256, RegistrationsSHA256 string
	IndexSHA256, StatusSHA256                          string
}

// MaterializationObservation is preservation evidence for an explicit repair.
// A dirty or advanced registered checkout is observable, not resettable. Its
// contents are not a completed setup/checkpoint and cannot grant readiness.
type MaterializationObservation struct {
	Plan        Plan
	Directories []DirectoryObservation
	Members     []MemberObservation
}

type Inspector struct {
	Git     func(context.Context, string, ...string) (string, error)
	Timeout time.Duration
}

// Inspect reads the complete stored vector before any member effect. Git must
// be the caller's supervised runner; optional index writes and fsmonitor are
// disabled even when the repository enables them. No source reselection occurs.
func (i Inspector) Inspect(ctx context.Context, plan Plan) (MaterializationObservation, error) {
	var zero MaterializationObservation
	data, err := ValidateMaterialization(plan)
	if err != nil {
		return zero, err
	}
	if i.Git == nil {
		return zero, errors.New("materialization inspection requires supervised Git")
	}
	timeout := i.Timeout
	if timeout == 0 {
		timeout = repository.DefaultTimeout
	}
	if timeout < 0 || timeout > time.Hour {
		return zero, errors.New("invalid materialization inspection timeout")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	plan.Members = append([]Member(nil), plan.Members...)
	o := MaterializationObservation{Plan: plan}
	for n, path := range []string{data, filepath.Join(data, "mirrors"), filepath.Join(data, "benches"), plan.Root} {
		d, err := observeDirectory(path, n >= 2)
		if err != nil {
			return zero, err
		}
		o.Directories = append(o.Directories, d)
	}
	if o.Directories[3].Present {
		entries, err := os.ReadDir(plan.Root)
		if err != nil {
			return zero, err
		}
		allowed := map[string]bool{}
		for _, member := range plan.Members {
			allowed[member.Name] = true
		}
		for _, entry := range entries {
			if !allowed[entry.Name()] || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return zero, errors.New("workspace root contains an unfamiliar entry; preserve and reconcile")
			}
		}
	}
	for _, member := range plan.Members {
		m, err := i.inspectMember(ctx, member)
		if err != nil {
			return zero, fmt.Errorf("workspace member %s: %w", member.Name, err)
		}
		o.Members = append(o.Members, m)
	}
	// Named roots may have changed while Git ran. Refuse a mixed observation.
	for _, d := range o.Directories {
		actual, err := observeDirectory(d.Path, !d.Present)
		if err != nil || actual != d {
			return zero, errors.New("workspace storage changed during inspection")
		}
	}
	for _, m := range o.Members {
		for _, d := range []DirectoryObservation{m.Directory, m.Mirror, m.Administration} {
			if d.Path == "" {
				continue
			}
			actual, err := observeDirectory(d.Path, !d.Present)
			if err != nil || actual != d {
				return zero, errors.New("workspace member identity changed during inspection")
			}
		}
	}
	return o, ctx.Err()
}

// Verify is a whole-vector comparison, never a partial-member permission. A
// caller must durably distinguish its own completed effects before accepting
// a new observation; unrelated late registrations/ref changes are conflicts.
func (i Inspector) Verify(ctx context.Context, approved MaterializationObservation) error {
	actual, err := i.Inspect(ctx, approved.Plan)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, approved) {
		return errors.New("workspace materialization observation changed")
	}
	return nil
}

func observeDirectory(path string, optional bool) (DirectoryObservation, error) {
	d := DirectoryObservation{Path: path}
	info, err := os.Lstat(path)
	if err != nil {
		if optional && errors.Is(err, syscall.ENOENT) {
			// Prove each existing ancestor below the fixed data root separately in
			// Inspect; ENOTDIR, permissions and symlinks are never absence.
			return d, nil
		}
		return d, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return d, errors.New("workspace path is not a physical directory")
	}
	st := info.Sys().(*syscall.Stat_t)
	d.Present, d.Device, d.Inode, d.Mode = true, uint64(st.Dev), st.Ino, uint32(info.Mode())
	return d, nil
}

func observationDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (i Inspector) readGit(ctx context.Context, dir string, args ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	flags := []string{"--no-optional-locks", "--no-replace-objects", "--literal-pathspecs", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "-c", "maintenance.auto=false"}
	out, err := i.Git(ctx, dir, append(flags, args...)...)
	if err != nil {
		return "", err
	}
	if len(out) > 1<<20 {
		return "", errors.New("workspace inspection Git output exceeds limit")
	}
	return out, ctx.Err()
}

func (i Inspector) inspectMember(ctx context.Context, m Member) (MemberObservation, error) {
	o := MemberObservation{Name: m.Name}
	var err error
	if o.Mirror, err = observeDirectory(m.Mirror, false); err != nil {
		return o, err
	}
	if o.Directory, err = observeDirectory(m.Path, true); err != nil {
		return o, err
	}
	git := func(dir string, args ...string) (string, error) {
		out, e := i.readGit(ctx, dir, args...)
		return strings.TrimSuffix(out, "\n"), e
	}
	// A read-only inspection must not fetch a missing promised object. Refuse
	// promisor configuration before asking Git to dereference the pinned source.
	config, err := i.readGit(ctx, m.Mirror, "config", "--null", "--list")
	if err != nil {
		return o, err
	}
	for _, row := range strings.Split(config, "\x00") {
		key, _, _ := strings.Cut(row, "\n")
		key = strings.ToLower(key)
		if key == "extensions.partialclone" || strings.HasPrefix(key, "remote.") && strings.HasSuffix(key, ".promisor") {
			return o, errors.New("materialization inspection refuses promisor repositories")
		}
	}
	for _, check := range []struct {
		args []string
		want string
	}{
		{[]string{"rev-parse", "--is-bare-repository"}, "true"},
		{[]string{"config", "--null", "--get-all", "remote.origin.url"}, m.Repository + "\x00"},
		{[]string{"rev-parse", "--show-object-format=storage"}, m.ObjectFormat},
		{[]string{"rev-parse", "--verify", m.Source + "^{commit}"}, m.Source},
	} {
		out, e := git(m.Mirror, check.args...)
		if e != nil {
			return o, e
		}
		if out != check.want {
			return o, errors.New("workspace mirror or pinned source differs")
		}
	}
	heads, err := i.readGit(ctx, m.Mirror, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/")
	if err != nil {
		return o, err
	}
	o.HeadsSHA256 = observationDigest([]byte(heads))
	ref := "refs/heads/" + m.Branch
	for _, line := range strings.Split(strings.TrimSuffix(heads, "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, " ")
		if len(fields) != 2 || !model.ValidBranchRef(fields[0]) || !validMemberCommit(m, fields[1]) {
			return o, errors.New("invalid workspace mirror head inventory")
		}
		if BranchNamespaceConflict(fields[0], ref) {
			return o, errors.New("workspace task branch namespace conflict")
		}
		if fields[0] == ref {
			o.TaskCommit = fields[1]
		}
	}
	registrations, err := i.readGit(ctx, m.Mirror, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return o, err
	}
	o.RegistrationsSHA256 = observationDigest([]byte(registrations))
	registered, err := observeRegistration(registrations, m)
	if err != nil {
		return o, err
	}
	if !o.Directory.Present {
		o.Disposition = "absent"
	} else {
		entries, e := os.ReadDir(m.Path)
		if e != nil {
			return o, e
		}
		if len(entries) == 0 {
			o.Disposition = "empty"
		} else {
			o.Disposition = "checkout"
		}
	}
	if o.Disposition != "checkout" {
		if registered != "" || o.TaskCommit != "" && o.TaskCommit != m.Source {
			return o, errors.New("unmaterialized target has conflicting task ownership")
		}
		return o, nil
	}
	if registered == "" {
		return o, errors.New("checkout lacks exact worktree registration")
	}
	for _, check := range []struct {
		args []string
		want string
	}{
		{[]string{"rev-parse", "--show-toplevel"}, m.Path},
		{[]string{"rev-parse", "--path-format=absolute", "--git-common-dir"}, m.Mirror},
		{[]string{"symbolic-ref", "--quiet", "HEAD"}, ref},
	} {
		out, e := git(m.Path, check.args...)
		if e != nil {
			return o, e
		}
		if out != check.want {
			return o, errors.New("checkout identity differs from stored vector")
		}
	}
	o.Head, err = git(m.Path, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return o, err
	}
	if !validMemberCommit(m, o.Head) || o.Head != registered || o.Head != o.TaskCommit {
		return o, errors.New("checkout HEAD differs from registration/task branch")
	}
	admin, err := git(m.Path, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return o, err
	}
	if filepath.Dir(filepath.Dir(admin)) != m.Mirror || filepath.Base(filepath.Dir(admin)) != "worktrees" {
		return o, errors.New("checkout administration is not in the registered mirror")
	}
	if _, err = observeDirectory(filepath.Dir(admin), false); err != nil {
		return o, err
	}
	if o.Administration, err = observeDirectory(admin, false); err != nil {
		return o, err
	}
	index, err := git(m.Path, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return o, err
	}
	if index != filepath.Join(admin, "index") {
		return o, errors.New("checkout index escaped registered administration")
	}
	if o.IndexSHA256, err = inspectIndex(ctx, index); err != nil {
		return o, err
	}
	status, err := i.readGit(ctx, m.Path, "status", "--porcelain=v1", "-z", "--ignored=matching", "--untracked-files=all", "--ignore-submodules=all")
	if err != nil {
		return o, err
	}
	o.StatusSHA256 = observationDigest([]byte(status))
	return o, nil
}

func validMemberCommit(m Member, c string) bool {
	return model.ValidCommit(c) && (m.ObjectFormat == "sha1" && len(c) == 40 || m.ObjectFormat == "sha256" && len(c) == 64)
}

func observeRegistration(raw string, m Member) (string, error) {
	if !strings.HasSuffix(raw, "\x00\x00") {
		return "", errors.New("invalid worktree registration inventory")
	}
	selected := ""
	for _, record := range strings.Split(strings.TrimSuffix(raw, "\x00\x00"), "\x00\x00") {
		var path, branch, head string
		bad := false
		for _, field := range strings.Split(record, "\x00") {
			switch {
			case strings.HasPrefix(field, "worktree "):
				if path != "" {
					return "", errors.New("duplicate registration path")
				}
				path = strings.TrimPrefix(field, "worktree ")
			case strings.HasPrefix(field, "branch "):
				if branch != "" {
					return "", errors.New("duplicate registration branch")
				}
				branch = strings.TrimPrefix(field, "branch ")
			case strings.HasPrefix(field, "HEAD "):
				if head != "" {
					return "", errors.New("duplicate registration HEAD")
				}
				head = strings.TrimPrefix(field, "HEAD ")
			case strings.HasPrefix(field, "prunable"), strings.HasPrefix(field, "locked"):
				bad = true
			}
		}
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return "", errors.New("invalid registration path")
		}
		if path != m.Path && branch != "refs/heads/"+m.Branch {
			continue
		}
		if selected != "" || path != m.Path || branch != "refs/heads/"+m.Branch || !validMemberCommit(m, head) || bad {
			return "", errors.New("workspace task has changed, stale or occupied registration")
		}
		selected = head
	}
	return selected, nil
}

func inspectIndex(ctx context.Context, path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return "", errors.New("workspace index is not a bounded regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if err != nil {
		return "", err
	}
	if len(raw) > 16<<20 {
		return "", errors.New("workspace index exceeds inspection limit")
	}
	named, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !os.SameFile(info, named) || info.Size() != named.Size() {
		return "", errors.New("workspace index changed during inspection")
	}
	return observationDigest(raw), ctx.Err()
}
