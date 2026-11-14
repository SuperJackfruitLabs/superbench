package reap

import (
	"errors"
	"path/filepath"
	"strings"
)

func rejectMounts(root string) error {
	actual, e := filepath.EvalSymlinks(root)
	if e != nil {
		return e
	}
	paths, e := mountPaths()
	if e != nil {
		return e
	}
	for _, path := range paths {
		if path == actual || strings.HasPrefix(path, actual+string(filepath.Separator)) {
			return errors.New("mounted reap root or nested mount refused")
		}
	}
	return nil
}
