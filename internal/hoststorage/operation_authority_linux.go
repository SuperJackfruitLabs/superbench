//go:build linux

package hoststorage

import (
	"bytes"
	"context"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storagecustody"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storageproof"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type operationRoot = storagecustody.OperationRoot

func operationRootInfo(path string, private bool) (operationRoot, error) {
	return storagecustody.OperationRootInfo(path, private)
}

// OperationAuthority reads only existing protected policy/ledger/anchor and the
// exact mounted root. It never applies quotas, creates metadata, or claims that
// an empty ledger lock proves writer quiescence. Assignment updates publish
// atomically; the complete ledger is validated, but its mutable allocation
// vector is excluded from this immutable helper authority digest.
func (p Policy) OperationAuthority(ctx context.Context) (string, error) {
	if os.Geteuid() != 0 {
		return "", errors.New("operation authority requires root")
	}
	if err := p.Validate(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	data, err := operationRootInfo(p.Root, false)
	if err != nil {
		return "", err
	}
	ledgerRoot, err := operationRootInfo(p.Ledger, true)
	if err != nil {
		return "", err
	}
	ledgerFS, err := storageproof.StateFilesystem(ctx, p.Ledger, ledgerRoot.Device)
	if err != nil {
		return "", err
	}
	var fs syscall.Statfs_t
	if err = syscall.Statfs(p.Root, &fs); err != nil {
		return "", err
	}
	if fs.Type != 0x58465342 {
		return "", errors.New("operation storage requires XFS")
	}
	parent, err := os.Stat(filepath.Dir(p.Root))
	if err != nil {
		return "", err
	}
	if uint64(parent.Sys().(*syscall.Stat_t).Dev) == data.Device {
		return "", errors.New("operation storage requires exact dedicated mount")
	}
	mount, uuid, err := managedStorageMount(ctx, p.Root, data.Device)
	if err != nil {
		return "", err
	}
	raw, err := readStorageFile(filepath.Join(p.Ledger, "assignments.json"), 16<<20)
	if err != nil {
		return "", err
	}
	anchor, err := readStorageFile(filepath.Join(p.Root, ".superbench-quota-owner.json"), 4096)
	if err != nil {
		return "", err
	}
	expected, header, err := p.operationAuthorityRecords(raw, anchor)
	if err != nil {
		return "", err
	}
	if uuid != expected {
		return "", errors.New("operation mount UUID differs from ledger")
	}
	state, err := storageCommand(ctx, "/usr/sbin/xfs_quota", "-x", "-c", "state -p", p.Root)
	if err != nil {
		return "", err
	}
	if !bytes.Contains(state, []byte("Accounting: ON")) || !bytes.Contains(state, []byte("Enforcement: ON")) {
		return "", errors.New("operation storage quota enforcement unavailable")
	}
	endMount, endUUID, err := managedStorageMount(ctx, p.Root, data.Device)
	if err != nil {
		return "", err
	}
	endData, err := operationRootInfo(p.Root, false)
	if err != nil {
		return "", err
	}
	endLedger, err := operationRootInfo(p.Ledger, true)
	if err != nil {
		return "", err
	}
	endLedgerFS, err := storageproof.StateFilesystem(ctx, p.Ledger, endLedger.Device)
	if err != nil {
		return "", err
	}
	if data != endData || ledgerRoot != endLedger || ledgerFS != endLedgerFS || mount != endMount || uuid != endUUID {
		return "", errors.New("operation storage authority changed during inspection")
	}
	digest, err := storageproof.AuthorityDigest(p, storageproof.Root(data), storageproof.Root(ledgerRoot), ledgerFS, mount, uuid, header, storageDigest(anchor))
	if err != nil {
		return "", err
	}
	return digest, ctx.Err()
}
