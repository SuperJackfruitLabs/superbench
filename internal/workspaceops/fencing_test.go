package workspaceops

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
)

// An execution recorded on a different host cannot be fenced by this process.
// This fixture grants no runtime authority and never creates or starts a scope.
func TestAdminFailedFenceDoesNotGrantPriorOwner(t *testing.T) {
	m, r := fixture(t)
	create(t, m, r)
	held, err := m.Execute(context.Background(), request("lease"), 42)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("c", 32)
	err = statestore.Within(m.Store.State, nil, func(session *statestore.Session) error {
		return session.Update(func(tx *sql.Tx) error {
			rows, err := benchstate.Load(tx)
			if err != nil {
				return err
			}
			b := rows["card"]
			b.Lease.Execution = &execution.Execution{ID: id, Root: "/unavailable-disposable-scope", Phase: "starting", HostID: "different-host", BootID: "different-boot", Log: filepath.Join(m.Store.State, "run-"+id+".log"), LogLimitBytes: 4096}
			if err := benchstate.Validate(rows); err != nil {
				return err
			}
			raw, err := json.Marshal(b)
			if err != nil {
				return err
			}
			_, err = tx.Exec("UPDATE benches SET record=? WHERE key=?", string(raw), b.Key)
			return err
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, m)
	admin := protocolv2.Request{Version: protocolv2.Version, Operation: "stop_admin", Key: "card"}
	out, err := m.Execute(context.Background(), admin, 42)
	if err == nil || out != nil {
		t.Fatal("failed fence exposed prior owner", err)
	}
	if !reflect.DeepEqual(before, snapshot(t, m)) {
		t.Fatal("failed fence changed ownership or execution evidence")
	}
	if err := m.Store.CheckWorkspaceOwner("card", held.GrantToken, "worker"); err != nil {
		t.Fatal("failed fence replaced token", err)
	}
}
