package workspaceidle

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/checkpointset"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshtree"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacereclaim"
)

const data = "/srv/bench-data"

var start = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

// record is a released, allocated, checkpointed workspace, idle since idle.
func record(t *testing.T, key string, idle time.Time) *model.Bench {
	t.Helper()
	root := filepath.Join(data, "benches", key)
	repo := "/srv/source.git"
	b := &model.Bench{Key: key, Path: root, ProjectID: 10007, QuotaBytes: 4096, QuotaInodes: 128, RequestedQuotaBytes: 4096, Workspace: &model.Workspace{ID: fmt.Sprintf("%032x", sha256.Sum256([]byte(key)))[:32], CreationID: "request1", CheckpointRepository: "api", Phase: "ready", ExplicitQuotaBytes: 4096,
		Members: []model.WorkspaceMember{{Name: "api", Repository: repo, Branch: "bench/" + key, Base: "main", Path: filepath.Join(root, "api"), Mirror: repository.Path(data, repo), Source: strings.Repeat("d", 40), ObjectFormat: "sha1", RequestedQuotaBytes: 4096}}}}
	c := &model.WorkspaceCheckpoint{GenerationID: strings.Repeat("e", 32), Members: []model.WorkspaceCommit{{Name: "api", Commit: strings.Repeat("d", 40)}}}
	m, err := workspacepolicy.Manifest(b, c)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	c.Digest, c.Seal = checkpointset.Digest(raw), strings.Repeat("f", 40)
	b.Workspace.Checkpoint = c
	if !idle.IsZero() {
		b.Workspace.IdleSince = idle.Format(time.RFC3339Nano)
	}
	if err = workspacepolicy.Validate(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// store is an in-memory daemon state behind one lock, whose updates pass the
// real record and transition validation, as the SQLite store's do.
type store struct {
	rows    benchstate.Database
	commits int
	held    bool
}

func (s *store) within(_ context.Context, fn func(workspace.Update) error) error {
	if s.held {
		return errors.New("host lock re-entered")
	}
	s.held = true
	defer func() { s.held = false }()
	return fn(func(change workspace.Change) error {
		next := benchstate.Database{}
		for k, b := range s.rows {
			next[k] = workspace.Clone(b)
		}
		before := benchstate.Capture(next)
		if err := change(next, nil); err != nil {
			return err
		}
		if err := benchstate.Validate(next); err != nil {
			return err
		}
		if err := benchstate.ValidateTransition(before, next); err != nil {
			return err
		}
		s.rows = next
		s.commits++
		return nil
	})
}

type tree struct{ freshtree.Tree }

// harness wires the sweep to the real workspace reclaim, with the helper,
// readiness and admission stubbed, as the operator route wires it.
type harness struct {
	s         *store
	sweep     Sweep
	now       time.Time
	legacy    map[string]bool
	helper    []string
	helperErr []error
	// between runs after selection and before each reclaim, as a concurrent
	// request admitted between them would.
	between func(key string)
}

func newHarness(rows ...*model.Bench) *harness {
	h := &harness{s: &store{rows: benchstate.Database{}}, now: start, legacy: map[string]bool{}}
	for _, b := range rows {
		h.s.rows[b.Key] = b
	}
	reclaim := workspacereclaim.Manager{State: "/srv/state", Data: data, Git: func(context.Context, string, ...string) (string, error) { return "", nil },
		Fresh:    func(b *model.Bench) (freshtree.Tree, error) { return tree{}, nil },
		Admit:    func(context.Context, *sql.Tx, *model.Bench) error { return nil },
		Released: func(context.Context, *model.Bench) error { return nil },
		Helper: func(_ context.Context, b *model.Bench, _ payloadbootstrap.Spec) error {
			h.helper = append(h.helper, b.Key)
			if len(h.helperErr) != 0 {
				err := h.helperErr[0]
				h.helperErr = h.helperErr[1:]
				return err
			}
			return nil
		}}
	h.sweep = Sweep{State: "/srv/state", Grace: 24 * time.Hour, Now: func() time.Time { return h.now }, Within: h.s.within,
		Registered: func(b *model.Bench) (bool, error) { return !h.legacy[b.Key], nil },
		Reclaim: func(ctx context.Context, key string, eligible func(*model.Bench) error) error {
			if h.between != nil {
				h.between(key)
			}
			m := reclaim
			m.Within = func(fn func(workspace.Update) error) error { return h.s.within(ctx, fn) }
			m.Eligible = eligible
			_, err := m.Reclaim(ctx, key)
			return err
		}}
	return h
}

func (h *harness) phase(key string) string { return h.s.rows[key].Workspace.Phase }

func TestSelectTakesOnlyReleasedFreshCheckpointedBenchesIdlePastTheGrace(t *testing.T) {
	old := start.Add(-25 * time.Hour)
	leased := record(t, "leased", old)
	leased.Lease = &model.Lease{Holder: "worker", Token: strings.Repeat("a", 48), Continuation: strings.Repeat("b", 32)}
	h := newHarness(record(t, "idle", old), record(t, "recent", start.Add(-23*time.Hour)), record(t, "edge", start.Add(-24*time.Hour)),
		record(t, "legacy", old), record(t, "untimed", time.Time{}), record(t, "legacy-untimed", time.Time{}), leased)
	h.legacy["legacy"], h.legacy["legacy-untimed"] = true, true
	keys, err := h.sweep.Select(context.Background())
	if err != nil || !reflect.DeepEqual(keys, []string{"idle"}) {
		t.Fatalf("selected %v %v", keys, err)
	}
	// An old record without the time is idle from first sight, never before.
	if got := h.s.rows["untimed"].Workspace.IdleSince; got != start.Format(time.RFC3339Nano) {
		t.Fatalf("untimed record stamped %q", got)
	}
	if h.s.rows["legacy"].Workspace.IdleSince != old.Format(time.RFC3339Nano) || h.s.rows["legacy-untimed"].Workspace.IdleSince != "" {
		t.Fatal("a legacy bench's idle time changed")
	}
	h.now = start.Add(24*time.Hour + time.Second)
	if keys, err = h.sweep.Select(context.Background()); err != nil || !reflect.DeepEqual(keys, []string{"edge", "idle", "recent", "untimed"}) {
		t.Fatalf("after a day: %v %v", keys, err)
	}
	for _, grace := range []time.Duration{0, time.Minute} {
		if _, err := (Sweep{Grace: grace, Within: h.s.within}).Select(context.Background()); err == nil {
			t.Errorf("grace %v accepted", grace)
		}
	}
}

func TestTickReclaimsAndRetriesARefusedReclaimOnTheNextTick(t *testing.T) {
	h := newHarness(record(t, "card", start.Add(-25*time.Hour)))
	h.helperErr = []error{errors.New("the bench is in use")}
	results, err := h.sweep.Tick(context.Background())
	if err != nil || len(results) != 1 || results[0].Status != "failed" || h.phase("card") != "reclaiming" {
		t.Fatalf("first tick: %+v %v %s", results, err, h.phase("card"))
	}
	// The pending reclaim is durable; the next tick resumes it, grace or not.
	h.now = start
	h.s.rows["card"].Workspace.IdleSince = start.Format(time.RFC3339Nano)
	results, err = h.sweep.Tick(context.Background())
	if err != nil || len(results) != 1 || results[0].Status != "reclaimed" || h.phase("card") != "reclaimed" || !reflect.DeepEqual(h.helper, []string{"card", "card"}) {
		t.Fatalf("second tick: %+v %v %v", results, err, h.helper)
	}
	if results, err = h.sweep.Tick(context.Background()); err != nil || len(results) != 0 {
		t.Fatalf("a reclaimed bench was selected again: %+v %v", results, err)
	}
}

// Selection and reclaim are separate host-lock sections. Whatever a request
// does between them, the reclaim decides again inside its own: an acquisition
// that holds the lease, or one that acquires and releases again (which moves
// the idle time), refuses the reclaim before anything is recorded.
func TestAnAcquisitionBetweenSelectionAndReclaimWins(t *testing.T) {
	lease := &model.Lease{Holder: "worker", Token: strings.Repeat("a", 48), Continuation: strings.Repeat("b", 32)}
	for name, between := range map[string]func(*model.Bench){
		"held":     func(b *model.Bench) { b.Lease = lease },
		"released": func(b *model.Bench) { b.Workspace.IdleSince = start.Format(time.RFC3339Nano) },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(record(t, "card", start.Add(-25*time.Hour)))
			h.between = func(key string) {
				err := h.s.within(context.Background(), func(update workspace.Update) error {
					return update(func(rows benchstate.Database, _ *sql.Tx) error { between(rows[key]); return nil })
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			before := h.s.commits
			results, err := h.sweep.Tick(context.Background())
			if err != nil || len(results) != 1 || results[0].Status != "refused" || h.phase("card") != "ready" || len(h.helper) != 0 || h.s.commits != before+2 {
				t.Fatalf("%+v %v %s %v %d", results, err, h.phase("card"), h.helper, h.s.commits-before)
			}
		})
	}
}

// The other order: once a reclaim is recorded, no acquisition can take the
// record, because a lease is granted only to a ready record.
func TestAReclaimingRecordCannotBeLeased(t *testing.T) {
	h := newHarness(record(t, "card", start.Add(-25*time.Hour)))
	h.helperErr = []error{errors.New("lost reply")}
	if _, err := h.sweep.Tick(context.Background()); err != nil || h.phase("card") != "reclaiming" {
		t.Fatal(err, h.phase("card"))
	}
	err := h.s.within(context.Background(), func(update workspace.Update) error {
		return update(func(rows benchstate.Database, _ *sql.Tx) error {
			rows["card"].Lease = &model.Lease{Holder: "worker", Token: strings.Repeat("a", 48), Continuation: strings.Repeat("b", 32)}
			return nil
		})
	})
	if err == nil {
		t.Fatal("a reclaiming record was leased")
	}
}

// Each part of the released shape is required on its own.
func TestReleasedRequiresEveryCondition(t *testing.T) {
	lease := &model.Lease{Holder: "worker", Token: strings.Repeat("a", 48)}
	if !released(record(t, "card", start)) {
		t.Fatal("a released record was not released")
	}
	for name, change := range map[string]func(*model.Bench){
		"phase":      func(b *model.Bench) { b.Workspace.Phase = "archived" },
		"lease":      func(b *model.Bench) { b.Lease = lease },
		"pending":    func(b *model.Bench) { b.Workspace.Pending = &model.WorkspaceCheckpoint{} },
		"stage":      func(b *model.Bench) { b.Workspace.Stage = &model.WorkspaceStage{} },
		"checkpoint": func(b *model.Bench) { b.Workspace.Checkpoint = nil },
		"seal":       func(b *model.Bench) { b.Workspace.Checkpoint.Seal = "" },
		"archive":    func(b *model.Bench) { b.Workspace.Archive = &model.WorkspaceArchive{} },
		"transfer":   func(b *model.Bench) { b.Workspace.Transfer = &model.WorkspaceTransfer{} },
		"repair":     func(b *model.Bench) { b.Workspace.InitializationRepair = &model.WorkspaceInitializationRepair{} },
		"project":    func(b *model.Bench) { b.ProjectID = 0 },
		"scalar":     func(b *model.Bench) { b.Workspace = nil },
	} {
		b := record(t, "card", start)
		change(b)
		if released(b) {
			t.Errorf("%s: released", name)
		}
	}
}
