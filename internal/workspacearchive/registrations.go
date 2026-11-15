package workspacearchive

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

// Registration binds the administration directory outside the reviewed checkout
// tree. Deleting source files never grants authority over a destination entry.
type Registration struct {
	Name, Path, Admin    string
	Device, Inode        uint64
	Mode                 uint32
	HEAD, Gitdir, Common string
}

func registrationFile(path string) (string, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 8192 {
		return "", errors.New("invalid worktree registration metadata")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 8193))
	if len(raw) > 8192 {
		return "", errors.New("oversized registration metadata")
	}
	return string(raw), err
}
func registrationIdentity(member model.WorkspaceMember, admin string) (Registration, error) {
	r := Registration{Name: member.Name, Path: member.Path, Admin: admin}
	mirror, err := filepath.EvalSymlinks(member.Mirror)
	if err != nil {
		return r, err
	}
	physical, err := filepath.EvalSymlinks(admin)
	if err != nil {
		return r, err
	}
	if physical != admin || filepath.Dir(admin) != filepath.Join(mirror, "worktrees") {
		return r, errors.New("worktree admin path changed")
	}
	info, err := os.Lstat(admin)
	if err != nil {
		return r, err
	}
	if !info.IsDir() {
		return r, errors.New("worktree administration is not a directory")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return r, errors.New("unsupported registration identity")
	}
	r.Device, r.Inode, r.Mode = uint64(st.Dev), uint64(st.Ino), uint32(info.Mode())
	if _, err = os.Lstat(filepath.Join(admin, "locked")); !os.IsNotExist(err) {
		return r, errors.New("aggregate worktree is locked")
	}
	r.HEAD, err = registrationFile(filepath.Join(admin, "HEAD"))
	if err != nil {
		return r, err
	}
	r.Gitdir, err = registrationFile(filepath.Join(admin, "gitdir"))
	if err != nil {
		return r, err
	}
	r.Common, err = registrationFile(filepath.Join(admin, "commondir"))
	if err != nil {
		return r, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(member.Path))
	if err != nil {
		return r, err
	}
	canonical := filepath.Join(parent, filepath.Base(member.Path), ".git")
	if r.Gitdir != filepath.Join(member.Path, ".git")+"\n" && r.Gitdir != canonical+"\n" {
		return r, errors.New("worktree registration backlink changed")
	}
	common, err := filepath.EvalSymlinks(filepath.Join(admin, strings.TrimSpace(r.Common)))
	if err != nil || common != mirror {
		return r, errors.New("worktree registration common directory changed")
	}
	return r, nil
}
func (m Manager) registrationSnapshot(ctx context.Context, b *model.Bench) ([]Registration, error) {
	var result []Registration
	for _, member := range b.Workspace.Members {
		admin, err := m.Git(ctx, member.Path, "rev-parse", "--absolute-git-dir")
		if err != nil {
			return nil, err
		}
		r, err := registrationIdentity(member, strings.TrimSpace(admin))
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, nil
}

// registrations never prunes. Missing registrations are accepted on retry only
// when the approved exact administration directory is also absent.
func (m Manager) registrations(ctx context.Context, b *model.Bench, missing, remove bool, approved ...[]Registration) error {
	data, err := filepath.EvalSymlinks(m.Data)
	if err != nil {
		return err
	}
	transferred := b.Workspace.Transfer != nil && b.Workspace.Transfer.Phase == "transferred"
	if len(approved) > 0 && len(approved[0]) == 0 {
		if b.Workspace.Transfer != nil {
			return errors.New("transferred reap requires approved registrations")
		}
		return m.legacyRegistrations(ctx, b, missing, remove)
	}
	var proof []Registration
	if len(approved) > 0 {
		proof = approved[0]
		if len(proof) != len(b.Workspace.Members) {
			return errors.New("incomplete approved registrations")
		}
	}
	for i, member := range b.Workspace.Members {
		out, err := m.Git(ctx, member.Mirror, "worktree", "list", "--porcelain", "-z")
		if err != nil {
			return err
		}
		canonical := filepath.Join(data, "benches", b.Key, member.Name)
		selected := ""
		for _, record := range strings.Split(out, "\x00\x00") {
			var path, branch, head string
			locked, detached := false, false
			for _, field := range strings.Split(record, "\x00") {
				switch {
				case strings.HasPrefix(field, "worktree "):
					path = strings.TrimPrefix(field, "worktree ")
				case strings.HasPrefix(field, "branch "):
					branch = strings.TrimPrefix(field, "branch ")
				case strings.HasPrefix(field, "HEAD "):
					head = strings.TrimPrefix(field, "HEAD ")
				case field == "detached":
					detached = true
				case field == "locked" || strings.HasPrefix(field, "locked "):
					locked = true
				}
			}
			target := path == member.Path || path == canonical
			if !transferred && branch == "refs/heads/"+member.Branch && !target {
				return errors.New("aggregate task branch registered to another worktree")
			}
			if !target {
				continue
			}
			valid := branch == "refs/heads/"+member.Branch && !detached
			if transferred {
				valid = branch == "" && detached
			}
			if selected != "" || !valid || locked || head != b.Workspace.Checkpoint.Members[i].Commit {
				return errors.New("aggregate worktree registration changed or is locked")
			}
			selected = path
		}
		if selected == "" {
			if !missing {
				return errors.New("aggregate member worktree registration is missing")
			}
			if proof == nil {
				return errors.New("missing registration requires exact approved identity")
			}
			if _, err = os.Lstat(proof[i].Admin); !os.IsNotExist(err) {
				return errors.New("unlisted approved registration still exists")
			}
			continue
		}
		var r Registration
		if proof != nil {
			if proof[i].Name != member.Name || proof[i].Path != member.Path {
				return errors.New("approved registration member changed")
			}
			r, err = registrationIdentity(member, proof[i].Admin)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(r, proof[i]) {
				return errors.New("approved worktree registration was replaced or changed")
			}
		} else {
			admin, e := m.Git(ctx, member.Path, "rev-parse", "--absolute-git-dir")
			if e != nil {
				return e
			}
			r, err = registrationIdentity(member, strings.TrimSpace(admin))
			if err != nil {
				return err
			}
		}
		expected := "ref: refs/heads/" + member.Branch + "\n"
		if transferred {
			expected = b.Workspace.Checkpoint.Members[i].Commit + "\n"
		}
		if r.HEAD != expected {
			return errors.New("registered HEAD changed")
		}
		if !remove {
			continue
		}
		if _, err = os.Lstat(member.Path); !os.IsNotExist(err) {
			if err != nil {
				return err
			}
			return errors.New("aggregate member path reappeared after approved deletion")
		}
		// --force touches only an already absent checkout with its exact approved
		// registration. In particular it never selects by the now-shared task ref.
		if _, err = m.Git(ctx, member.Mirror, "worktree", "remove", "--force", selected); err != nil {
			return err
		}
	}
	return nil
}
