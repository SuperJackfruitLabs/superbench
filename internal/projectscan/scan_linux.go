//go:build linux

package projectscan

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaleaf"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storagecustody"
)

// Scan walks every allocated bench's tree below root as root, through
// descriptors only, and reports each. It changes nothing. It returns
// ErrOutside, after reporting every bench, when any is outside its project.
func Scan(root, ledger string) ([]Result, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("project scan requires root")
	}
	for _, dir := range []string{root, filepath.Join(root, "benches")} {
		if err := storagecustody.RootOwnedDirectory(dir, false); err != nil {
			return nil, err
		}
	}
	raw, err := storagecustody.ReadProtected(filepath.Join(ledger, "assignments.json"), 16<<20)
	if err != nil {
		return nil, err
	}
	list, err := benches(root, raw)
	if err != nil {
		return nil, err
	}
	parent, err := os.OpenFile(filepath.Join(root, "benches"), os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	var results []Result
	counts := map[string]int{}
	for _, b := range list {
		if b.retired {
			continue
		}
		r := Result{Key: b.key, ProjectID: b.project, Fresh: b.fresh}
		err := scanOne(parent, b)
		if r.Status = classify(err); errors.Is(err, errAbsent) {
			r.Status = "absent"
		} else if err != nil {
			r.Reason = err.Error()
		}
		counts[r.Status]++
		results = append(results, r)
	}
	switch {
	case counts["outside"] != 0:
		return results, ErrOutside
	case counts["unverified"] != 0:
		return results, ErrUnverified
	}
	return results, nil
}

// errAbsent is a bench whose leaf does not exist: nothing to walk.
var errAbsent = errors.New("bench leaf absent")

func scanOne(parent *os.File, b bench) error {
	fd, err := syscall.Openat(int(parent.Fd()), b.key, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ENOENT) {
		return errAbsent
	}
	if err != nil {
		return err
	}
	leaf := os.NewFile(uintptr(fd), b.path)
	defer leaf.Close()
	return quotaleaf.Tree(leaf, b.project, int(b.inodes))
}
