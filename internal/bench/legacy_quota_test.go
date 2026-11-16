package bench

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
)

func TestLegacyAllocatedCreationRetainsOriginalDefaultQuota(t *testing.T) {
	for _, replyRecorded := range []bool{true, false} {
		name := "recorded-helper-reply"
		if !replyRecorded {
			name = "lost-helper-reply"
		}
		t.Run(name, func(t *testing.T) { legacyAllocatedCreationRetainsQuota(t, replyRecorded) })
	}
}

func legacyAllocatedCreationRetainsQuota(t *testing.T, replyRecorded bool) {
	t.Helper()
	s, remote, _ := managedFixture(t)
	seed := filepath.Join(filepath.Dir(remote), "seed")
	if err := os.Mkdir(filepath.Join(seed, ".bench"), 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(seed, ".bench/config.toml"), "[quota]\nsize='3KiB'\n")
	g(t, seed, "add", ".")
	g(t, seed, "commit", "-m", "previously unsupported quota config")
	g(t, seed, "push", "origin", "main")
	mirror, err := s.prepareMirror(context.Background(), remote)
	if err != nil {
		t.Fatal(err)
	}
	source := g(t, mirror, "rev-parse", "refs/remotes/origin/main^{commit}")
	key := "legacy"
	// The previous binary allocated its default before worktree materialization.
	// It knew no quota request field, even if this source already contained one.
	intent := creation{
		Bench: Bench{Key: key, Repo: remote, Branch: "bench/legacy", Mirror: mirror,
			Path: filepath.Join(s.Data, "benches", key), ProjectID: 10000, QuotaBytes: 8 << 20, QuotaInodes: 128},
		Source: source, Base: "main",
	}
	if !replyRecorded {
		// The privileged helper committed the default allocation, but its reply
		// never reached the caller. Zero journal fields cannot prove no allocation.
		intent.Bench.ProjectID = 0
		intent.Bench.QuotaBytes = 0
		intent.Bench.QuotaInodes = 0
	}
	if err = os.MkdirAll(intent.Bench.Path, 0700); err != nil {
		t.Fatal(err)
	}
	if err = s.record(&intent); err != nil {
		t.Fatal(err)
	}
	original := s.quotaApply
	calls := 0
	s.quotaApply = func(c HostConfig, kind, key string, requested uint64) (hoststorage.Assignment, error) {
		if kind == "bench" {
			calls++
			if requested != 0 {
				return hoststorage.Assignment{}, fmt.Errorf("attempted to resize legacy allocation to %d", requested)
			}
		}
		return original(c, kind, key, requested)
	}
	b, err := s.CreateColdContext(context.Background(), key, Options{Repo: remote}, false)
	if err != nil {
		t.Fatalf("legacy allocated creation did not resume: %v", err)
	}
	if calls != 1 || b.RequestedQuotaBytes != 0 || b.QuotaBytes != 8<<20 || b.ProjectID != 10000 || b.QuotaInodes != 128 {
		t.Fatalf("legacy quota allocation changed: calls=%d, bench=%+v", calls, b)
	}
	if got := g(t, b.Path, "rev-parse", "HEAD"); got != source {
		t.Fatalf("resumed source %s, want %s", got, source)
	}
	pending, err := s.pending(key, Options{Repo: remote, Branch: "bench/legacy", Base: "main"}, false)
	if err != nil || pending == nil || !pending.QuotaRequestPinned || pending.Bench.RequestedQuotaBytes != 0 {
		t.Fatalf("upgraded creation intent not durably pinned to default: %+v, %v", pending, err)
	}
}
