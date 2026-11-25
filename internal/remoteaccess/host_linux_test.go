package remoteaccess

import "testing"

// The probe finds mount_setattr on every kernel CI runs (5.12 or later):
// only ENOSYS refuses, so EBADF and a seccomp EPERM both pass.
func TestProbeFindsMountSetattr(t *testing.T) {
	if err := probeMountSetattr(); err != nil {
		t.Fatal(err)
	}
}

// The installed host refuses roots in the system's /tmp, which is where
// t.TempDir puts them unless TMPDIR moves it.
func TestInstalledHostRefusesRootsUnderTmp(t *testing.T) {
	_, store := hostFixture(t)
	store.Data = "/tmp/superbench-data"
	refusedFor(t, installedHost.check(store), "data root /tmp/superbench-data must not be under /tmp")
}
