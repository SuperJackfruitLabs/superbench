package retention

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUnregisteredLogsRequireSeparateOwnershipApproval(t *testing.T) {
	root, db := fixture(t)
	id := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	name, _ := Name(id)
	path := filepath.Join(root, name)
	if e := os.WriteFile(path, []byte("legacy output"), 0600); e != nil {
		t.Fatal(e)
	}
	p, e := Build(db, root, nil, 0, 0, 100)
	if e != nil || len(p.Logs) != 0 {
		t.Fatal("unregistered file automatically eligible", e)
	}
	if _, e = Inspect(db, root, id, map[string]bool{id: true}); e == nil {
		t.Fatal("held legacy log inspected for adoption")
	}
	inspection, e := Inspect(db, root, id, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = transaction(db, func(tx *sql.Tx) error { _, e := Adopt(tx, root, *inspection, map[string]bool{id: true}); return e }); e == nil {
		t.Fatal("held legacy log adopted")
	}
	for i := 0; i < 2; i++ {
		if e = transaction(db, func(tx *sql.Tx) error {
			r, e := Adopt(tx, root, *inspection, nil)
			if e == nil && (r.Origin != "operator-adopted" || r.ExitCode != nil || !r.StartedAt.IsZero()) {
				t.Error("adoption invented execution outcome")
			}
			return e
		}); e != nil {
			t.Fatal(e)
		}
	}
	p, e = Build(db, root, nil, 0, time.Hour, 100)
	if e != nil || len(p.Logs) != 1 {
		t.Fatal("approved ownership did not enable log review", e)
	}
}
