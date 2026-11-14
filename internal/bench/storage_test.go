package bench

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
)

func fakeSpace(s *Store) {
	s.spaceProbe = func(path string) (Space, error) {
		device := uint64(1)
		if strings.HasPrefix(path, s.Data) {
			device = 2
		}
		return Space{Free: 1 << 30, Total: 2 << 30, Device: device}, nil
	}
}
func managedFixture(t *testing.T) (Store, string, *int) {
	t.Helper()
	s, r := fixture(t)
	fakeSpace(&s)
	calls := 0
	s.quotaApply = func(_ HostConfig, kind, key string) (hoststorage.Assignment, error) {
		path := filepath.Join(s.Data, key)
		id := uint32(100)
		if kind == "bench" {
			path = filepath.Join(s.Data, "benches", key)
			id = 10000
			calls++
		}
		if e := os.MkdirAll(path, 0700); e != nil {
			return hoststorage.Assignment{}, e
		}
		return hoststorage.Assignment{Path: path, ProjectID: id, Bytes: 8 << 20}, nil
	}
	config := HostConfig{DataRoot: s.Data, QuotaHelper: "/test/helper", MinDataFreeBytes: 1 << 20, MinStateFreeBytes: 1 << 20}
	if e := s.ConfigureHost(config); e != nil {
		t.Fatal(e)
	}
	return s, r, &calls
}
func TestManagedCreateRecordsQuotaAndResumesEmptyAllocation(t *testing.T) {
	s, r, calls := managedFixture(t)
	original := s.quotaApply
	failed := false
	s.quotaApply = func(c HostConfig, kind, key string) (hoststorage.Assignment, error) {
		a, e := original(c, kind, key)
		if e == nil && kind == "bench" && !failed {
			failed = true
			return a, errors.New("lost reply after privileged allocation")
		}
		return a, e
	}
	if _, e := s.Create("task", Options{Repo: r}, false); e == nil {
		t.Fatal("injected allocation failure ignored")
	}
	b, e := s.Create("task", Options{Repo: r}, false)
	if e != nil {
		t.Fatal(e)
	}
	if b.ProjectID != 10000 || b.QuotaBytes != 8<<20 || *calls != 2 {
		t.Fatal("allocation was not recorded/retried", b, *calls)
	}
	if g(t, b.Path, "rev-parse", "HEAD") == "" {
		t.Fatal("worktree missing")
	}
}
func TestAdmissionBlocksGrowthButAllowsReconciliation(t *testing.T) {
	s, r := fixture(t)
	s.spaceProbe = func(string) (Space, error) { return Space{}, nil }
	if _, e := s.Create("task", Options{Repo: r}, false); e == nil {
		t.Fatal("created at zero reserve")
	}
	if _, e := os.Stat(s.Data); !os.IsNotExist(e) {
		t.Fatal("low-space create touched data")
	}
	s.spaceProbe = nil
	b, e := s.Create("task", Options{Repo: r}, false)
	if e != nil {
		t.Fatal(e)
	}
	lease, e := s.Lease(b.Key, "writer")
	if e != nil {
		t.Fatal(e)
	}
	s.spaceProbe = func(string) (Space, error) { return Space{}, nil }
	if _, e = s.Checkpoint(b.Key, lease.Lease.Token); e != nil {
		t.Fatal("admission blocked checkpoint recovery", e)
	}
	if e = s.Release(b.Key, lease.Lease.Token); e != nil {
		t.Fatal("admission blocked release recovery", e)
	}
	if _, e = s.Lease(b.Key, "next"); e == nil {
		t.Fatal("new lease acquired with no reserve")
	}
}
func TestManagedConfigurationPinsDataRoot(t *testing.T) {
	s, _, _ := managedFixture(t)
	wrong := s
	wrong.Data = s.Data + "-other"
	if _, e := wrong.HostStatus(); e == nil {
		t.Fatal("host config accepted another data root")
	}
	c, e := s.hostConfig()
	if e != nil {
		t.Fatal(e)
	}
	c.MinStateFreeBytes *= 2
	if e = s.ConfigureHost(*c); e == nil {
		t.Fatal("configuration silently changed")
	}
}
func TestCannotAdoptExistingBenchesOrSameFilesystem(t *testing.T) {
	s, r := fixture(t)
	if _, e := s.Create("task", Options{Repo: r}, false); e != nil {
		t.Fatal(e)
	}
	fakeSpace(&s)
	c := HostConfig{DataRoot: s.Data, QuotaHelper: "/test/helper", MinDataFreeBytes: 1024, MinStateFreeBytes: 1024}
	if e := s.ConfigureHost(c); e == nil {
		t.Fatal("adopted unmanaged benches")
	}
	s, r = fixture(t)
	_ = r
	s.spaceProbe = func(string) (Space, error) { return Space{Free: 1 << 30, Device: 1}, nil }
	c.DataRoot = s.Data
	if e := s.ConfigureHost(c); e == nil {
		t.Fatal("managed state/data share filesystem")
	}
}
func TestSetupRequiresFrozenUnambiguousLockfile(t *testing.T) {
	s, _ := fixture(t)
	root := t.TempDir()
	if _, e := s.setupCommand(root, false); e == nil {
		t.Fatal("guessed setup without a lockfile")
	}
	write(t, filepath.Join(root, "pnpm-lock.yaml"), "fixture")
	command, e := s.setupCommand(root, true)
	if e != nil {
		t.Fatal(e)
	}
	joined := strings.Join(command, " ")
	if !strings.Contains(joined, "--frozen-lockfile") || !strings.Contains(joined, "--package-import-method clone") || !strings.HasSuffix(joined, "--offline") {
		t.Fatal(command)
	}
	write(t, filepath.Join(root, "package-lock.json"), "{}")
	if _, e = s.setupCommand(root, false); e == nil {
		t.Fatal("ambiguous lockfiles accepted")
	}
	env := s.cacheEnvironment([]string{"npm_config_cache=/other", "BENCH_LEASE_TOKEN=private"})
	if len(env) != 5 {
		t.Fatal("duplicate override or leaked token", env)
	}
}
func TestPartialMirrorIsNotPublishedOrBorrowed(t *testing.T) {
	s, r := fixture(t)
	orphan := filepath.Join(s.Data, "mirrors", ".clone-abandoned", "repo.git")
	if e := os.MkdirAll(orphan, 0700); e != nil {
		t.Fatal(e)
	}
	write(t, filepath.Join(orphan, "partial"), "preserve")
	b, e := s.Create("task", Options{Repo: r}, false)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(b.Mirror, ".clone-") {
		t.Fatal("borrowed temporary objects")
	}
	raw, e := os.ReadFile(filepath.Join(orphan, "partial"))
	if e != nil || string(raw) != "preserve" {
		t.Fatal("unrelated orphan changed", e)
	}
	g(t, b.Mirror, "config", "remote.origin.url", r+"-wrong")
	if _, e = s.Create("other", Options{Repo: r}, false); e == nil {
		t.Fatal("mismatched mirror reused")
	}
}
