package retention

import (
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCollectionProcessHelper(t *testing.T) {
	root := os.Getenv("SUPERBENCH_TEST_COLLECTION_ROOT")
	if root == "" {
		return
	}
	raw, e := os.ReadFile(filepath.Join(root, "approved.json"))
	if e != nil {
		t.Fatal(e)
	}
	var p Plan
	if e = json.Unmarshal(raw, &p); e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("sqlite", filepath.Join(root, "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	e = transaction(db, func(tx *sql.Tx) error {
		_, e := Apply(tx, root, p, nil, func() error {
			if e := os.WriteFile(filepath.Join(root, "unlink-ready"), []byte("ready"), 0600); e != nil {
				return e
			}
			for {
				time.Sleep(time.Second)
			}
		})
		return e
	})
	t.Fatalf("helper returned before SIGKILL: %v", e)
}
func TestCollectionSurvivesProcessSIGKILLAfterUnlink(t *testing.T) {
	root, db := fixture(t)
	r := record(t, root, db, 1)
	p, e := Build(db, root, nil, 0, 0, 100)
	if e != nil {
		t.Fatal(e)
	}
	if e = transaction(db, func(tx *sql.Tx) error { return Prepare(tx, root, *p, nil) }); e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(p)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(root, "approved.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	db.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCollectionProcessHelper$")
	cmd.Env = append(os.Environ(), "SUPERBENCH_TEST_COLLECTION_ROOT="+root)
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	killed := false
	defer func() {
		if !killed {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, e = os.Stat(filepath.Join(root, "unlink-ready")); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not reach unlink boundary")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e = cmd.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	if e = cmd.Wait(); e == nil {
		t.Fatal("child was not killed")
	}
	killed = true
	db, e = sql.Open("sqlite", filepath.Join(root, "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	c, saved, e := GetCollection(db, p.Digest)
	if e != nil || c.Status != "prepared" || saved == nil {
		t.Fatal("approval did not survive process death", e)
	}
	if e = transaction(db, func(tx *sql.Tx) error {
		c, e := Apply(tx, root, *saved, nil, nil)
		if e == nil && c.Status != "completed" {
			t.Error("not completed")
		}
		return e
	}); e != nil {
		t.Fatal(e)
	}
	name, _ := Name(r.ID)
	if _, e = os.Stat(filepath.Join(root, name)); !os.IsNotExist(e) {
		t.Fatal("log reappeared after recovery")
	}
}
