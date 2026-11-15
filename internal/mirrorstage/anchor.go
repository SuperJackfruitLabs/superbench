package mirrorstage

import (
	"errors"
	"os"
	"path/filepath"
)

// boundDir keeps metadata and durability operations on the same opened directory.
// Named checks refuse replacements; they do not claim isolation from hostile writers.
type boundDir struct {
	path string
	root *os.Root
	file *os.File
	id   Identity
}

func bind(path string) (*boundDir, error) {
	expected, e := identity(path)
	if e != nil {
		return nil, e
	}
	root, e := os.OpenRoot(path)
	if e != nil {
		return nil, e
	}
	return bindRoot(path, root, expected)
}
func bindRoot(path string, root *os.Root, expected Identity) (*boundDir, error) {
	file, e := root.Open(".")
	if e != nil {
		root.Close()
		return nil, e
	}
	info, e := file.Stat()
	if e != nil {
		file.Close()
		root.Close()
		return nil, e
	}
	actual, e := fileIdentity(info)
	d := &boundDir{path: path, root: root, file: file, id: actual}
	if e != nil || actual != expected {
		d.close()
		return nil, errors.New("opened staging directory differs from named identity")
	}
	if e = d.check(); e != nil {
		d.close()
		return nil, e
	}
	return d, nil
}
func (d *boundDir) child(name string) (*boundDir, error) {
	if filepath.Base(name) != name || name == "." || name == ".." {
		return nil, errors.New("fixed staging leaf required")
	}
	if e := d.check(); e != nil {
		return nil, e
	}
	info, e := d.root.Lstat(name)
	if e != nil {
		return nil, e
	}
	expected, e := fileIdentity(info)
	if e != nil {
		return nil, e
	}
	root, e := d.root.OpenRoot(name)
	if e != nil {
		return nil, e
	}
	child, e := bindRoot(filepath.Join(d.path, name), root, expected)
	if e != nil {
		return nil, e
	}
	if e = d.check(); e != nil {
		child.close()
		return nil, e
	}
	return child, nil
}
func (d *boundDir) check() error {
	current, e := identity(d.path)
	if e != nil {
		return e
	}
	info, e := d.file.Stat()
	if e != nil {
		return e
	}
	actual, e := fileIdentity(info)
	if e != nil {
		return e
	}
	if current != d.id || actual != d.id {
		return errors.New("staging directory identity changed")
	}
	return nil
}
func (d *boundDir) sync() error {
	if e := d.file.Sync(); e != nil {
		return e
	}
	return d.check()
}
func (d *boundDir) close() { d.file.Close(); d.root.Close() }

func openJournal(state *boundDir, create bool) (*boundDir, error) {
	if e := state.check(); e != nil {
		return nil, e
	}
	if create {
		if e := state.root.Mkdir("mirror-stages", 0700); e != nil && !os.IsExist(e) {
			return nil, e
		}
		if e := state.sync(); e != nil {
			return nil, e
		}
	}
	d, e := state.child("mirror-stages")
	if e != nil {
		return nil, e
	}
	if os.FileMode(d.id.Mode).Perm() != 0700 {
		d.close()
		return nil, errors.New("private mirror staging journal required")
	}
	return d, nil
}
