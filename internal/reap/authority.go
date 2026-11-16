package reap

import (
	"errors"
	"os"
)

var ErrRoot = errors.New("reap must not run as root")

// RequireNonRoot applies to administrative deletion, never the fixed quota
// helper's separately constrained empty-allocation retirement.
func RequireNonRoot() error {
	if os.Geteuid() == 0 {
		return ErrRoot
	}
	return nil
}

func openTree(path string) (*os.Root, error) {
	named, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !named.IsDir() || named.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("reap root must be a physical directory")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(named, opened) {
		root.Close()
		return nil, errors.New("reap root identity changed")
	}
	return root, nil
}
func checkTreeName(path string, root *os.Root) error {
	named, err := os.Lstat(path)
	if err != nil {
		return err
	}
	opened, err := root.Stat(".")
	if err != nil {
		return err
	}
	if !named.IsDir() || !os.SameFile(named, opened) {
		return errors.New("reap root identity changed")
	}
	return nil
}
