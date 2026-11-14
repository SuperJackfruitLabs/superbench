package reap

import (
	"errors"
	"os"
	"path/filepath"
)

func File(root, name string) (Entry, error) {
	if filepath.Base(name) != name || name == "." || name == ".." {
		return Entry{}, errors.New("file name must be one path component")
	}
	if e := rejectMounts(filepath.Join(root, name)); e != nil {
		return Entry{}, e
	}
	entry, e := inspect(root, name, false)
	if e != nil {
		return entry, e
	}
	if !os.FileMode(entry.Mode).IsRegular() {
		return entry, errors.New("retained log must be a regular file")
	}
	return entry, nil
}
