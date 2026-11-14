package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/retention"
	"os"
	"testing"
	"time"
)

type fakeStore struct {
	root        string
	items       []retention.Item
	calls       []retention.Plan
	collections []retention.Collection
	fail        bool
	entered     chan struct{}
	release     chan struct{}
}

func (f *fakeStore) PlanCollection(logAge, metaAge time.Duration, limit int) (*retention.Plan, error) {
	now := time.Now().UTC()
	p := &retention.Plan{Version: "superbench.logs.v1", Root: f.root, CreatedAt: now, LogCutoff: now.Add(-logAge), MetadataCutoff: now.Add(-metaAge), Logs: append([]retention.Item(nil), f.items...)}
	var e error
	p.Digest, e = p.Hash()
	return p, e
}
func (f *fakeStore) Collect(p retention.Plan) (retention.Collection, error) {
	f.calls = append(f.calls, p)
	if f.entered != nil {
		close(f.entered)
		<-f.release
		f.entered = nil
	}
	c := retention.Collection{Digest: p.Digest, Status: "prepared", Count: len(p.Logs)}
	if f.fail {
		f.collections = []retention.Collection{c}
		return c, errors.New("interrupted prepared collection")
	}
	c.Status = "completed"
	f.collections = []retention.Collection{c}
	f.items = nil
	return c, nil
}
func (f *fakeStore) Collections() ([]retention.Collection, error) { return f.collections, nil }
func fixture(t *testing.T) (Manager, *fakeStore, Policy) {
	t.Helper()
	root := t.TempDir()
	if e := os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	id := fmt.Sprintf("%032x", 1)
	entry := reap.Entry{Path: "run-" + id + ".log", Mode: 0600, SHA256: "hash"}
	r := history.Record{ID: id, Key: "task", Origin: "supervised", Holder: "worker", StartedAt: time.Now().UTC().Add(-72 * time.Hour), ReleasedAt: time.Now().UTC().Add(-48 * time.Hour), SealedAt: time.Now().UTC().Add(-48 * time.Hour), File: &entry, Size: 100}
	hash, e := history.Digest(r)
	if e != nil {
		t.Fatal(e)
	}
	store := &fakeStore{root: root, items: []retention.Item{{Record: r, RecordDigest: hash, File: entry}}}
	p := Policy{Version: "superbench.retention-policy.v1", Root: root, Scope: Scope, LogAge: Duration(time.Hour), MetadataAge: Duration(24 * time.Hour), Limit: 10, MaxBytes: 1024}
	return Manager{Root: root, Store: store}, store, p
}
func TestNoAutomaticEffectsWithoutExplicitPolicyEnable(t *testing.T) {
	m, store, p := fixture(t)
	r, e := m.Tick(context.Background())
	if e != nil || r.Status != "disabled" || len(store.calls) != 0 {
		t.Fatal(r, e)
	}
	proposal, e := m.Preview(p)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Enable(*proposal, false); e == nil {
		t.Fatal("missing enable acknowledgment accepted")
	}
	forged := *proposal
	forged.Policy.Scope = "all-files"
	forged.Digest, _ = forged.Hash()
	if _, e = m.Enable(forged, true); e == nil {
		t.Fatal("widened scope accepted")
	}
	if _, e = m.Enable(*proposal, true); e != nil {
		t.Fatal(e)
	}
	r, e = m.Tick(context.Background())
	if e != nil || r.Status != "completed" || len(store.calls) != 1 {
		t.Fatal(r, e)
	}
	if _, e = m.Disable(); e != nil {
		t.Fatal(e)
	}
	r, e = m.Tick(context.Background())
	if e != nil || r.Status != "disabled" || len(store.calls) != 1 {
		t.Fatal("disabled policy collected", e)
	}
	raw, e := json.Marshal(p)
	if e != nil {
		t.Fatal(e)
	}
	var wire map[string]any
	if e = json.Unmarshal(raw, &wire); e != nil {
		t.Fatal(e)
	}
	if wire["LogAge"] != "1h0m0s" {
		t.Fatal("age not reviewable", string(raw))
	}
}
func TestChangedInitialCandidatesRequireFreshReview(t *testing.T) {
	m, store, p := fixture(t)
	proposal, e := m.Preview(p)
	if e != nil {
		t.Fatal(e)
	}
	store.items = nil
	if _, e = m.Enable(*proposal, true); e == nil {
		t.Fatal("stale concrete preview activated")
	}
	if len(store.calls) != 0 {
		t.Fatal("activation deleted data")
	}
}
func TestInterruptedBatchRetainsExactApprovalAndDisableStopsRetry(t *testing.T) {
	m, store, p := fixture(t)
	proposal, e := m.Preview(p)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Enable(*proposal, true); e != nil {
		t.Fatal(e)
	}
	store.fail = true
	if _, e = m.Tick(context.Background()); e == nil {
		t.Fatal("interruption ignored")
	}
	status, e := m.Status()
	if e != nil || status.Pending == nil {
		t.Fatal("pending approval not durable", e)
	}
	digest := status.Pending.Plan.Digest
	if _, e = m.Disable(); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Tick(context.Background()); e != nil || len(store.calls) != 1 {
		t.Fatal("disabled policy resumed", e)
	}
	if _, e = m.AcknowledgePending(); e == nil {
		t.Fatal("prepared core approval discarded")
	}
	// Reenable the same rule/preview, then retry its exact persisted batch.
	// Real prepared records no longer appear in fresh candidate discovery.
	store.items = nil
	if _, e = m.Enable(*proposal, true); e != nil {
		t.Fatal(e)
	}
	store.fail = false
	if _, e = m.Tick(context.Background()); e != nil {
		t.Fatal(e)
	}
	if len(store.calls) != 2 || store.calls[1].Digest != digest {
		t.Fatal("interruption generated fresh deletion authority")
	}
	status, e = m.Status()
	if e != nil || status.Pending != nil {
		t.Fatal("completed pending pointer retained", e)
	}
}
func TestByteBoundAndPendingPolicyValidation(t *testing.T) {
	m, store, p := fixture(t)
	store.items[0].Record.Size = 2048
	preview, e := m.Preview(p)
	if e != nil {
		t.Fatal(e)
	}
	if len(preview.Initial.Logs) != 0 {
		t.Fatal("oversize log approved for automatic deletion")
	}
	store.items[0].Record.Size = 100
	preview, e = m.Preview(p)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Enable(*preview, true); e != nil {
		t.Fatal(e)
	}
	bad := preview.Initial
	bad.LogCutoff = bad.CreatedAt
	bad.Digest, _ = bad.Hash()
	if e = m.write("retention-auto-pending.json", Pending{PolicyDigest: preview.Digest, Plan: bad}); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Tick(context.Background()); e == nil || len(store.calls) != 0 {
		t.Fatal("pending batch widened approved age")
	}
}
func TestDisableWaitsForCurrentBatchThenBlocksFurtherWork(t *testing.T) {
	m, store, p := fixture(t)
	proposal, e := m.Preview(p)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Enable(*proposal, true); e != nil {
		t.Fatal(e)
	}
	store.entered = make(chan struct{})
	store.release = make(chan struct{})
	entered := store.entered
	tickDone := make(chan error, 1)
	go func() { _, e := m.Tick(context.Background()); tickDone <- e }()
	<-entered
	disableDone := make(chan error, 1)
	go func() { _, e := m.Disable(); disableDone <- e }()
	select {
	case e := <-disableDone:
		t.Fatal("disable returned during active batch", e)
	default:
	}
	close(store.release)
	if e := <-tickDone; e != nil {
		t.Fatal(e)
	}
	if e := <-disableDone; e != nil {
		t.Fatal(e)
	}
	if _, e = m.Tick(context.Background()); e != nil || len(store.calls) != 1 {
		t.Fatal("work continued after disable", e)
	}
}
