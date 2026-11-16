//go:build linux

// Package storagebootstrap implements only first-install, nonallocating quota
// metadata publication. It does not prove service quiescence or adopt old data.
package storagebootstrap

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperationjournal"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storagecustody"
)

var ErrFresh = errors.New("fresh storage initialization refused")
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Fresh retains physical root and ledger-parent descriptors until Close.
// Callers still need the independent actual stopped-service installation gate.
type Fresh struct {
	policy               hoststorage.Policy
	root, parent         *os.File
	rootInfo, parentInfo os.FileInfo
	mount, uuid          string
}

func Inspect(ctx context.Context, p hoststorage.Policy) (*Fresh, error) {
	if os.Getuid() != 0 || os.Geteuid() != 0 || os.Getenv("SUDO_UID") != "" || p.Validate() != nil || p.OperationJournal != "indexed-v2" {
		return nil, ErrFresh
	}
	f := &Fresh{policy: p}
	var err error
	defer func() {
		if err != nil {
			f.Close()
		}
	}()
	if err = storagecustody.RootOwnedDirectory(p.Root, false); err != nil {
		return nil, err
	}
	if err = storagecustody.RootOwnedDirectory(filepath.Dir(p.Ledger), true); err != nil {
		return nil, err
	}
	f.root, err = os.OpenFile(p.Root, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f.parent, err = os.OpenFile(filepath.Dir(p.Ledger), os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f.rootInfo, err = f.root.Stat()
	if err != nil {
		return nil, err
	}
	f.parentInfo, err = f.parent.Stat()
	if err != nil {
		return nil, err
	}
	f.mount, f.uuid, err = f.mounted(ctx)
	if err != nil {
		return nil, err
	}
	if err = f.Check(ctx); err != nil {
		return nil, err
	}
	if err = f.Empty(); err != nil {
		return nil, err
	}
	return f, nil
}

func (f *Fresh) Close() {
	if f == nil {
		return
	}
	if f.root != nil {
		f.root.Close()
		f.root = nil
	}
	if f.parent != nil {
		f.parent.Close()
		f.parent = nil
	}
}

func (f *Fresh) Check(ctx context.Context) error {
	if f == nil || f.root == nil || f.parent == nil || os.Getuid() != 0 || os.Geteuid() != 0 {
		return ErrFresh
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, p := range []struct {
		path    string
		before  os.FileInfo
		fd      *os.File
		private bool
	}{
		{f.policy.Root, f.rootInfo, f.root, false}, {filepath.Dir(f.policy.Ledger), f.parentInfo, f.parent, true},
	} {
		if err := storagecustody.RootOwnedDirectory(p.path, p.private); err != nil {
			return err
		}
		named, err := os.Lstat(p.path)
		if err != nil {
			return err
		}
		current, err := p.fd.Stat()
		if err != nil {
			return err
		}
		if !os.SameFile(p.before, named) || !os.SameFile(p.before, current) || p.before.Mode() != named.Mode() {
			return ErrFresh
		}
	}
	mount, uuid, err := f.mounted(ctx)
	if err != nil {
		return err
	}
	if mount != f.mount || uuid != f.uuid {
		return ErrFresh
	}
	return nil
}

// Empty requires leaf ENOENT: ENOTDIR, inaccessible parents and symbolic links
// are never treated as absence. Existing format intent is independently refused
// by quotaoperationjournal before it creates any new history.
func (f *Fresh) Empty() error {
	if f == nil || f.root == nil || f.parent == nil {
		return ErrFresh
	}
	if _, err := f.root.Seek(0, io.SeekStart); err != nil {
		return err
	}
	names, err := f.root.Readdirnames(1)
	if len(names) != 0 || !errors.Is(err, io.EOF) {
		return ErrFresh
	}
	_, err = os.Lstat(f.policy.Ledger)
	if !errors.Is(err, syscall.ENOENT) {
		return ErrFresh
	}
	return nil
}

// Initialize is legal only inside the real initializer's expiring callback.
// It never allocates a project, changes quota limits or invokes ApplyRequested.
// Any partial record is retained; there is no first-install retry adoption.
func (f *Fresh) Initialize(ctx context.Context, fresh quotaoperationjournal.FreshLedger) error {
	if err := f.Check(ctx); err != nil {
		return err
	}
	if _, err := f.root.Seek(0, io.SeekStart); err != nil {
		return err
	}
	names, err := f.root.Readdirnames(1)
	if len(names) != 0 || !errors.Is(err, io.EOF) {
		return ErrFresh
	}
	header, anchor, err := f.policy.FreshAllocationRecords(fresh, f.uuid)
	if err != nil {
		return err
	}
	ledger, err := os.OpenFile(f.policy.Ledger, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer ledger.Close()
	dev, ino, err := fresh.Identity()
	if err != nil {
		return err
	}
	info, err := ledger.Stat()
	if err != nil {
		return err
	}
	st := info.Sys().(*syscall.Stat_t)
	if uint64(st.Dev) != dev || st.Ino != ino || uint64(st.Dev) == uint64(f.rootInfo.Sys().(*syscall.Stat_t).Dev) {
		return ErrFresh
	}
	names, err = ledger.Readdirnames(2)
	if len(names) != 1 || names[0] != "operations.lock" || err != nil && !errors.Is(err, io.EOF) {
		return ErrFresh
	}
	if err = writeNew(ledger, "assignments.json", header); err != nil {
		return err
	}
	if err = f.Check(ctx); err != nil {
		return err
	}
	if err = writeNew(f.root, ".superbench-quota-owner.json", anchor); err != nil {
		return err
	}
	if err = f.Check(ctx); err != nil {
		return err
	}
	_, err = f.policy.OperationAuthority(ctx)
	return err
}

func writeNew(parent *os.File, name string, raw []byte) error {
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	n, err := f.Write(raw)
	if err != nil {
		return err
	}
	if n != len(raw) {
		return io.ErrShortWrite
	}
	if err = f.Sync(); err != nil {
		return err
	}
	return parent.Sync()
}

func command(ctx context.Context, path string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.WaitDelay = time.Second
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	var b limitedOutput
	cmd.Stdout = &b
	cmd.Stderr = io.Discard
	err := cmd.Run()
	if b.over {
		return "", ErrFresh
	}
	return b.String(), err
}

type limitedOutput struct {
	bytes.Buffer
	over bool
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 65536 {
		b.over = true
		return 0, ErrFresh
	}
	return b.Buffer.Write(p)
}

func (f *Fresh) mounted(ctx context.Context) (string, string, error) {
	var fs syscall.Statfs_t
	if err := syscall.Fstatfs(int(f.root.Fd()), &fs); err != nil {
		return "", "", err
	}
	if fs.Type != 0x58465342 {
		return "", "", ErrFresh
	}
	root := f.rootInfo.Sys().(*syscall.Stat_t)
	parent, err := os.Stat(filepath.Dir(f.policy.Root))
	if err != nil {
		return "", "", err
	}
	if root.Dev == parent.Sys().(*syscall.Stat_t).Dev {
		return "", "", ErrFresh
	}
	raw, err := command(ctx, "/usr/bin/findmnt", "--noheadings", "--raw", "--output", "ID,UUID,FSROOT,TARGET,MAJ:MIN", "--mountpoint", f.policy.Root)
	if err != nil {
		return "", "", err
	}
	fields := strings.Fields(raw)
	dev := uint64(root.Dev)
	major := (dev>>8)&0xfff | (dev>>32)&0xfffff000
	minor := dev&0xff | (dev>>12)&0xffffff00
	if len(fields) != 5 || fields[2] != "/" || fields[3] != f.policy.Root || fields[4] != strconv.FormatUint(major, 10)+":"+strconv.FormatUint(minor, 10) {
		return "", "", ErrFresh
	}
	if id, err := strconv.ParseUint(fields[0], 10, 64); err != nil || id == 0 {
		return "", "", ErrFresh
	}
	if !uuidPattern.MatchString(fields[1]) {
		return "", "", ErrFresh
	}
	quota, err := command(ctx, "/usr/sbin/xfs_quota", "-x", "-c", "state -p", f.policy.Root)
	if err != nil {
		return "", "", err
	}
	if !strings.Contains(quota, "Accounting: ON") || !strings.Contains(quota, "Enforcement: ON") {
		return "", "", ErrFresh
	}
	for _, unit := range []string{"-b", "-i"} {
		report, err := command(ctx, "/usr/sbin/xfs_quota", "-x", "-c", "report -p -n "+unit+" -N", f.policy.Root)
		if err != nil {
			return "", "", err
		}
		if err = freshProjects(report); err != nil {
			return "", "", err
		}
	}
	return strings.Join(fields, " "), strings.ToLower(fields[1]), nil
}

// Even zero-usage old project entries retain allocation provenance. A fresh
// filesystem may report only its default project, with no configured caps.
func freshProjects(raw string) error {
	seen := false
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if seen || len(fields) < 4 || fields[0] != "#0" {
			return ErrFresh
		}
		seen = true
		for _, value := range fields[1:4] {
			if _, err := strconv.ParseUint(value, 10, 64); err != nil {
				return ErrFresh
			}
		}
		if fields[2] != "0" || fields[3] != "0" {
			return ErrFresh
		}
	}
	return nil
}
