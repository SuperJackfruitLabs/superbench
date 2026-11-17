package workspacearchive

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
)

// A missing-source freeze retained before a reboot renumbered the data device
// still verifies its parent and root by inode and mode on this boot's device.
func TestMissingFreezeSurvivesARenumberedDevice(t *testing.T) {
	data, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(data, "benches", "source")
	if err = os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	ps, rs := parent.Sys().(*syscall.Stat_t), root.Sys().(*syscall.Stat_t)
	f := MissingFreeze{Path: path, Root: hoststorage.RetirementRoot{Device: uint64(rs.Dev) + 7, Inode: uint64(rs.Ino), Mode: uint32(root.Mode()),
		ParentDevice: uint64(ps.Dev) + 7, ParentInode: uint64(ps.Ino), ParentMode: uint32(parent.Mode())}}
	if err = checkMissingRoot(f, false); err != nil {
		t.Fatalf("freeze from before a renumbering refused: %v", err)
	}
	f.Root.Inode++
	if err = checkMissingRoot(f, false); err == nil {
		t.Fatal("another root accepted")
	}
}
