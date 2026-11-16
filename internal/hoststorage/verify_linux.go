//go:build linux

package hoststorage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// VerifyStorage inspects only an already allocated logical bench. In particular
// it never calls Apply, creates a lock/ledger/anchor, or touches the bench path.
func (p Policy) VerifyStorage(ctx context.Context, key string) (StorageIdentity, error) {
	return p.verifyStorage(ctx, key, false, nil)
}

// locked is private: callers must already hold the existing ledger lock.
func (p Policy) verifyStorage(ctx context.Context, key string, locked bool, original *Assignment) (StorageIdentity, error) {
	return p.inspectStorage(ctx, key, locked, original, false)
}

func (p Policy) InspectAllocation(ctx context.Context, key string) (AllocationObservation, error) {
	s, err := p.inspectStorage(ctx, key, false, nil, true)
	s.Version = "superbench.allocation-observation.v1"
	return AllocationObservation(s), err
}

func (p Policy) inspectStorage(ctx context.Context, key string, locked bool, original *Assignment, allowAbsent bool) (StorageIdentity, error) {
	var zero StorageIdentity
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if os.Geteuid() != 0 {
		return zero, errors.New("quota verification requires root")
	}
	if err := p.Validate(); err != nil {
		return zero, err
	}
	if !Key.MatchString(key) {
		return zero, errors.New("invalid bench key")
	}
	if err := rootOwnedDirectory(p.Root, false); err != nil {
		return zero, err
	}
	if err := rootOwnedDirectory(p.Ledger, true); err != nil {
		return zero, err
	}
	benches := filepath.Join(p.Root, "benches")
	if _, err := os.Lstat(benches); !allowAbsent || !errors.Is(err, syscall.ENOENT) {
		if err := rootOwnedDirectory(benches, false); err != nil {
			return zero, err
		}
	}
	device, inode, err := physicalStorageRoot(p.Root)
	if err != nil {
		return zero, err
	}
	var fs syscall.Statfs_t
	if err = syscall.Statfs(p.Root, &fs); err != nil {
		return zero, err
	}
	if fs.Type != 0x58465342 {
		return zero, errors.New("managed storage is not XFS")
	}
	parent, err := os.Stat(filepath.Dir(p.Root))
	if err != nil {
		return zero, err
	}
	if uint64(parent.Sys().(*syscall.Stat_t).Dev) == device {
		return zero, errors.New("managed storage is not a dedicated mount")
	}
	if !locked {
		lock, e := openStorageFile(filepath.Join(p.Ledger, "lock"))
		if e != nil {
			return zero, e
		}
		defer lock.Close()
		if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); e != nil {
			return zero, errors.New("quota ledger is busy")
		}
		defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	}
	raw, err := readStorageFile(filepath.Join(p.Ledger, "assignments.json"), 16<<20)
	if err != nil {
		return zero, err
	}
	anchor, err := readStorageFile(filepath.Join(p.Root, ".superbench-quota-owner.json"), 4096)
	if err != nil {
		return zero, err
	}
	var a Assignment
	var ledgerDigest string
	if original == nil {
		a, ledgerDigest, err = p.inspectStorageRecords(key, raw, anchor, allowAbsent)
	} else {
		a, ledgerDigest, _, err = p.retirementRecords(key, raw, anchor, *original)
	}
	if err != nil {
		return zero, err
	}
	var l ledger
	if err = strictStorageJSON(raw, &l); err != nil {
		return zero, err
	}
	mount, uuid, err := managedStorageMount(ctx, p.Root, device)
	if err != nil {
		return zero, err
	}
	if uuid != l.FilesystemUUID {
		return zero, errors.New("mounted filesystem differs from ledger UUID")
	}
	state, err := storageCommand(ctx, "/usr/sbin/xfs_quota", "-x", "-c", "state -p", p.Root)
	if err != nil {
		return zero, err
	}
	if !bytes.Contains(state, []byte("Accounting: ON")) || !bytes.Contains(state, []byte("Enforcement: ON")) {
		return zero, errors.New("project quotas are not accounting and enforcing")
	}
	for _, unit := range []string{"-b", "-i"} {
		if a.ProjectID == 0 {
			break
		}
		command := fmt.Sprintf("report -p -n %s -N -L %d -U %d", unit, a.ProjectID, a.ProjectID)
		report, e := storageCommand(ctx, "/usr/sbin/xfs_quota", "-x", "-c", command, p.Root)
		if e != nil {
			return zero, e
		}
		want := a.Inodes
		if unit == "-b" {
			want = a.Bytes / 1024
		}
		if e = verifyStorageQuota(report, a.ProjectID, want); e != nil {
			return zero, e
		}
	}
	endMount, endUUID, err := managedStorageMount(ctx, p.Root, device)
	if err != nil {
		return zero, err
	}
	endDevice, endInode, err := physicalStorageRoot(p.Root)
	if err != nil {
		return zero, err
	}
	if mount != endMount || uuid != endUUID || endDevice != device || endInode != inode {
		return zero, errors.New("mount changed during storage verification")
	}
	policy, _ := json.Marshal(p)
	return StorageIdentity{Version: storageVersion, Root: p.Root, BenchKey: key, Device: device, Inode: inode, Mount: mount, Managed: true, FilesystemUUID: uuid, PolicySHA256: storageDigest(policy), LedgerSHA256: ledgerDigest, AnchorSHA256: storageDigest(anchor), Assignment: a}, ctx.Err()
}

func openStorageFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Sys().(*syscall.Stat_t).Uid != 0 {
		f.Close()
		return nil, errors.New("storage authority must be a protected root-owned regular file")
	}
	return f, nil
}

func readStorageFile(path string, max int64) ([]byte, error) {
	f, err := openStorageFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > max {
		return nil, errors.New("storage authority exceeds size limit")
	}
	return raw, nil
}

func managedStorageMount(ctx context.Context, root string, device uint64) (string, string, error) {
	raw, err := storageCommand(ctx, "/usr/bin/findmnt", "--noheadings", "--raw", "--output", "ID,UUID,FSROOT,TARGET,MAJ:MIN", "--mountpoint", root)
	if err != nil {
		return "", "", err
	}
	return parseManagedStorageMount(raw, root, device)
}

func parseManagedStorageMount(raw []byte, root string, device uint64) (string, string, error) {
	f := strings.Fields(string(raw))
	major := (device>>8)&0xfff | (device>>32)&0xfffff000
	minor := device&0xff | (device>>12)&0xffffff00
	if len(f) != 5 || !volumePattern.MatchString(f[1]) || f[2] != "/" || f[3] != root || f[4] != fmt.Sprintf("%d:%d", major, minor) {
		return "", "", errors.New("expected exact dedicated filesystem mount identity")
	}
	if id, err := strconv.ParseUint(f[0], 10, 64); err != nil || id == 0 {
		return "", "", errors.New("invalid mount ID")
	}
	return strings.Join(f, " "), strings.ToLower(f[1]), nil
}

func verifyStorageQuota(raw []byte, id uint32, want uint64) error {
	count := 0
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[0] != "#"+strconv.FormatUint(uint64(id), 10) {
			continue
		}
		count++
		hard, err := strconv.ParseUint(f[3], 10, 64)
		if err != nil || hard != want {
			return errors.New("kernel project cap differs from ledger")
		}
	}
	if count != 1 {
		return errors.New("kernel project quota could not be uniquely verified")
	}
	return nil
}

func unmanagedStorageMount(root string) (string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	device := uint64(info.Sys().(*syscall.Stat_t).Dev)
	major := (device>>8)&0xfff | (device>>32)&0xfffff000
	minor := device&0xff | (device>>12)&0xffffff00
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return "", err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return "", err
	}
	if len(raw) > 1<<20 {
		return "", errors.New("mount table exceeds verification limit")
	}
	best, depth := "", -1
	decode := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 || fields[2] != fmt.Sprintf("%d:%d", major, minor) {
			continue
		}
		mountpoint := decode.Replace(fields[4])
		if root != mountpoint && !strings.HasPrefix(root, strings.TrimSuffix(mountpoint, "/")+"/") {
			continue
		}
		if len(mountpoint) > depth {
			best, depth = line, len(mountpoint)
		}
	}
	if best == "" {
		return "", errors.New("actual storage mount could not be identified")
	}
	return "linux:" + best, nil
}
