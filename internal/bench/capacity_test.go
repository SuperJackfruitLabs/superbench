package bench

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
)

func TestCapacityRefusalRetainsOriginalCreateIntentWithoutAllocation(t *testing.T) {
	s, repository, _ := managedFixture(t)
	want := &CapacityError{NeededBytes: 6500, AvailableBytes: 6000, NeededInodes: 400, AvailableInodes: 900}
	calls := 0
	original := s.quotaApply
	s.quotaApply = func(_ HostConfig, kind, key string, requested uint64) (hoststorage.Assignment, error) {
		if kind != "bench" {
			return original(HostConfig{}, kind, key, requested)
		}
		calls++
		if kind != "bench" || key != "card" || requested != 0 {
			t.Fatal("changed quota request", kind, key, requested)
		}
		return hoststorage.Assignment{}, want
	}
	options := Options{Repo: repository, Branch: "bench/card", Base: "main"}
	b, err := s.Create("card", options, false)
	var capacity *CapacityError
	if b != nil || !errors.As(err, &capacity) || capacity != want || calls != 1 {
		t.Fatal("capacity refusal lost or retried", b, err, calls)
	}
	pending, err := s.pending("card", options, false)
	if err != nil || pending == nil || pending.Bench.Key != "card" || pending.Bench.ProjectID != 0 || pending.Bench.QuotaBytes != 0 || pending.Bench.QuotaInodes != 0 {
		t.Fatal("original creation intent lost or granted", pending, err)
	}
	if _, err := os.Lstat(filepath.Join(s.Data, "benches", "card")); !os.IsNotExist(err) {
		t.Fatal("refusal created allocation", err)
	}
	if calls != 1 {
		t.Fatal("reading retained intent reexecuted helper", calls)
	}
}
