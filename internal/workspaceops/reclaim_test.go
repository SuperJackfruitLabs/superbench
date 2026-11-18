package workspaceops

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	_ "modernc.org/sqlite"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshtree"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshtree/freshtreetest"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacereclaim"
)

// The legacy path is unchanged: reclaim refuses a legacy workspace as a
// conflict, and a registered Fresh workspace without an installed helper
// route refuses closed, both with no state change.
func TestReclaimRefusesLegacyAndUninstalledFreshWithoutEffects(t *testing.T) {
	m, r := fixture(t)
	create(t, m, r)
	reclaim := protocolv2.Request{Version: protocolv2.Version, Operation: "reclaim", Key: "card"}
	before := snapshot(t, m)
	if _, err := m.Execute(context.Background(), reclaim, 42); !errors.Is(err, ErrConflict) || !errors.Is(err, workspacereclaim.ErrLegacy) {
		t.Fatalf("legacy reclaim: %v", err)
	}
	if !reflect.DeepEqual(before, snapshot(t, m)) {
		t.Fatal("a refused reclaim changed the legacy workspace")
	}
	b := before[0]
	freshtreetest.Register(t, m.Store.State, &b)
	if _, err := m.Execute(context.Background(), reclaim, 42); !errors.Is(err, freshtree.ErrUnavailable) {
		t.Fatalf("uninstalled Fresh reclaim: %v", err)
	}
	if !reflect.DeepEqual(before, snapshot(t, m)) {
		t.Fatal("a refused reclaim changed the Fresh workspace")
	}
	if _, err := m.Execute(context.Background(), protocolv2.Request{Version: protocolv2.Version, Operation: "reclaim", Key: "other"}, 42); err == nil {
		t.Fatal("an unknown key reclaimed")
	}
}

// The sweep's route always carries its condition; without one it is invalid.
func TestReclaimIdleRequiresItsCondition(t *testing.T) {
	m, r := fixture(t)
	create(t, m, r)
	if _, err := m.ReclaimIdle(context.Background(), "card", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unconditioned idle reclaim: %v", err)
	}
}

// Through the service's create: replaying a reclaimed bench's own create
// returns it, still reclaimed, and a new create on the key reaches staging
// (which, without Fresh payload, refuses it) instead of the duplicate-key
// conflict. Neither changes the reclaimed record.
func TestCreateOnAReclaimedKey(t *testing.T) {
	m, r := fixture(t)
	create(t, m, r)
	db, err := sql.Open("sqlite", "file:"+filepath.Join(m.Store.State, "bench.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`UPDATE benches SET record=json_set(record, '$.Workspace.Phase', 'reclaimed', '$.ProjectID', 10007, '$.QuotaBytes', 1073741824, '$.QuotaInodes', 1000) WHERE key='card'`); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, m)
	if out, err := m.Execute(context.Background(), r, 42); err != nil || out.Workspace.Workspace.Phase != "reclaimed" || out.RootPath || out.GrantToken != "" {
		t.Fatalf("replay of the reclaimed create: %+v %v", out, err)
	}
	next := r
	next.RequestID = "creation-2"
	if _, err := m.Execute(context.Background(), next, 42); err == nil || errors.Is(err, ErrConflict) {
		t.Fatalf("a new create on the reclaimed key: %v", err)
	}
	if !reflect.DeepEqual(before, snapshot(t, m)) {
		t.Fatal("a create changed the reclaimed record")
	}
}
