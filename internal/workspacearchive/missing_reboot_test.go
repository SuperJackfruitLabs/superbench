package workspacearchive

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
)

// A managed missing-source freeze retained before a reboot renumbered the data
// device verifies its parent and root by inode and mode on this boot's device;
// unmanaged storage has no UUID, so its recorded devices are still compared.
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
	recorded := hoststorage.RetirementRoot{Device: uint64(rs.Dev), Inode: uint64(rs.Ino), Mode: uint32(root.Mode()),
		ParentDevice: uint64(ps.Dev), ParentInode: uint64(ps.Ino), ParentMode: uint32(parent.Mode())}
	for name, c := range map[string]struct {
		change           func(*hoststorage.RetirementRoot)
		managed, refused bool
	}{
		"same boot, managed":           {func(*hoststorage.RetirementRoot) {}, true, false},
		"same boot, unmanaged":         {func(*hoststorage.RetirementRoot) {}, false, false},
		"renumbered, managed":          {func(r *hoststorage.RetirementRoot) { r.Device += 7; r.ParentDevice += 7 }, true, false},
		"renumbered parent, unmanaged": {func(r *hoststorage.RetirementRoot) { r.ParentDevice += 7 }, false, true},
		"renumbered root, unmanaged":   {func(r *hoststorage.RetirementRoot) { r.Device += 7 }, false, true},
		"another root, managed":        {func(r *hoststorage.RetirementRoot) { r.Inode++ }, true, true},
		"another parent, managed":      {func(r *hoststorage.RetirementRoot) { r.ParentInode++ }, true, true},
		"another root mode, unmanaged": {func(r *hoststorage.RetirementRoot) { r.Mode ^= 0o070 }, false, true},
	} {
		f := MissingFreeze{Path: path, Root: recorded}
		c.change(&f.Root)
		if err := checkMissingRoot(f, c.managed, false); (err != nil) != c.refused {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRetirementParentComparesDeviceOnlyWhenUnmanaged(t *testing.T) {
	recorded := hoststorage.RetirementRoot{ParentDevice: 0x701, ParentInode: 64, ParentMode: 0x800001ed}
	renumbered := recorded
	renumbered.ParentDevice = 0x704
	if !sameRetirementParent(renumbered, recorded, true) {
		t.Error("managed parent refused after a renumbering")
	}
	if sameRetirementParent(renumbered, recorded, false) {
		t.Error("unmanaged parent on another device accepted")
	}
	for name, change := range map[string]func(*hoststorage.RetirementRoot){
		"inode": func(r *hoststorage.RetirementRoot) { r.ParentInode++ },
		"mode":  func(r *hoststorage.RetirementRoot) { r.ParentMode ^= 0o022 },
	} {
		other := renumbered
		change(&other)
		if sameRetirementParent(other, recorded, true) {
			t.Errorf("managed parent with another %s accepted", name)
		}
	}
}
