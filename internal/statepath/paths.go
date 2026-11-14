// Package statepath validates the separation of authoritative state and bench data.
package statepath

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Validate canonicalizes existing ancestors as well as existing directories to
// reject symlink overlap. It never creates state or data directories.
func Validate(statePath, dataPath string) error {
	for _, p := range []string{statePath, dataPath} {
		if !filepath.IsAbs(p) {
			return errors.New("state and data paths must be absolute")
		}
	}
	canonical := func(p string) (string, error) {
		var tail []string
		for {
			resolved, err := filepath.EvalSymlinks(p)
			if err == nil {
				for i := len(tail) - 1; i >= 0; i-- {
					resolved = filepath.Join(resolved, tail[i])
				}
				return resolved, nil
			}
			if !os.IsNotExist(err) {
				return "", err
			}
			tail = append(tail, filepath.Base(p))
			p = filepath.Dir(p)
		}
	}
	state, err := canonical(statePath)
	if err != nil {
		return err
	}
	data, err := canonical(dataPath)
	if err != nil {
		return err
	}
	for _, pair := range [][2]string{{state, data}, {data, state}} {
		rel, e := filepath.Rel(pair[0], pair[1])
		if e != nil {
			return e
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return errors.New("state and data directories must be separate, non-nested paths")
		}
	}
	return nil
}
