package bench

import (
	"errors"
	"os"
	"path/filepath"
)

// Publish only a complete bare mirror. Interrupted clone directories remain
// unreferenced and quota-accounted; no bench ever borrows objects from them.
func (s Store) prepareMirror(repo string) (string, error) {
	parent := filepath.Join(s.Data, "mirrors")
	if e := os.MkdirAll(parent, 0700); e != nil {
		return "", e
	}
	mirror := s.mirrorPath(repo)
	if _, e := os.Lstat(mirror); os.IsNotExist(e) {
		stage, e := os.MkdirTemp(parent, ".clone-")
		if e != nil {
			return "", e
		}
		defer os.RemoveAll(stage)
		candidate := filepath.Join(stage, "repo.git")
		if _, e = git(s.State, "clone", "--bare", "--no-hardlinks", "--", repo, candidate); e != nil {
			return "", e
		}
		if e = os.Rename(candidate, mirror); e != nil {
			return "", e
		}
		dir, e := os.Open(parent)
		if e != nil {
			return "", e
		}
		e = dir.Sync()
		dir.Close()
		if e != nil {
			return "", e
		}
	} else if e != nil {
		return "", e
	}
	stat, e := os.Lstat(mirror)
	if e != nil {
		return "", e
	}
	if !stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("mirror path is not a directory")
	}
	bare, e := git(mirror, "rev-parse", "--is-bare-repository")
	if e != nil {
		return "", e
	}
	if bare != "true" {
		return "", errors.New("mirror is not a bare repository")
	}
	origin, e := git(mirror, "config", "--get", "remote.origin.url")
	if e != nil {
		return "", e
	}
	if origin != repo {
		return "", errors.New("mirror origin differs from requested repository")
	}
	// Remote-tracking refs never overwrite a branch checked out in another bench.
	if _, e = git(mirror, "fetch", "--prune", "origin", "+refs/heads/*:refs/remotes/origin/*"); e != nil {
		return "", e
	}
	return mirror, nil
}
