package operationcancel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
)

func TestPersistentCancellationRefusesOldSchemaWithoutMigration(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, data := filepath.Join(root, "state"), filepath.Join(root, "data")
	if err = os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := statestore.Open(filepath.Join(state, "bench.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = benchstate.Migrate(tx, state, nil); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`PRAGMA user_version=25`); err != nil {
		t.Fatal(err)
	}
	m, err := Persistent(state, data, &fence{}, func(_, _ string, fn func() error) error { return fn() })
	if err != nil {
		t.Fatal(err)
	}
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 1, RequestID: "old", Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "create", Epoch: strings.Repeat("b", 32)}
	if _, err = m.Inspect(i); err == nil {
		t.Fatal("cancellation migrated unknown old activity")
	}
	var version int
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 25 {
		t.Fatal(version, err)
	}
	if _, err = os.Stat(data); !os.IsNotExist(err) {
		t.Fatal("inspection created data", err)
	}
}
func TestPersistentGateUsesCapturedPhysicalState(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, alias := filepath.Join(root, "state"), filepath.Join(root, "alias")
	if err = os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(state, alias); err != nil {
		t.Fatal(err)
	}
	var gateState string
	m, err := Persistent(alias, filepath.Join(root, "data"), &fence{}, func(actual, _ string, fn func() error) error { gateState = actual; return fn() })
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Gate("card", func() error { return nil }); err != nil || gateState != state || m.State != state {
		t.Fatal(gateState, m.State, err)
	}
}
