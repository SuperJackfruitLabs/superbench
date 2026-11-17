//go:build linux

package hoststorage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storagecustody"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storageproof"
	"io"
	"os"
	"path/filepath"
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
		if e = storageproof.VerifyQuota(report, a.ProjectID, want); e != nil {
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
	return storagecustody.OpenProtected(path)
}

func readStorageFile(path string, max int64) ([]byte, error) {
	return storagecustody.ReadProtected(path, max)
}

func managedStorageMount(ctx context.Context, root string, device uint64) (string, string, error) {
	raw, err := storageCommand(ctx, "/usr/bin/findmnt", "--noheadings", "--raw", "--output", storageproof.ManagedMountColumns, "--mountpoint", root)
	if err != nil {
		return "", "", err
	}
	return storageproof.ParseManagedMount(raw, root, device)
}

func unmanagedStorageMount(root string) (string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
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
	return storageproof.ParseUnmanagedMount(raw, root, uint64(info.Sys().(*syscall.Stat_t).Dev))
}
