package workspacetransfer_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reftransfer"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacetransfer"
)

type fixture struct {
	t      *testing.T
	store  bench.Store
	m      workspacetransfer.Manager
	spec   workspace.Spec
	source *model.Bench
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := gitcontrol.Command(context.Background(), dir, args...)
	c.Env = append(gitcontrol.Environment(os.Environ()), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func newFixture(t *testing.T) *fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	must(t, err)
	f := &fixture{t: t, store: bench.Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data")}, spec: workspace.Spec{Key: "source", CheckpointRepository: "a", QuotaBytes: 1 << 20}}
	for _, name := range []string{"a", "b"} {
		repo := filepath.Join(root, "repository-"+name)
		must(t, os.MkdirAll(repo, 0700))
		git(t, repo, "init", "-b", "main")
		must(t, os.WriteFile(filepath.Join(repo, "source.txt"), []byte(name+"\n"), 0600))
		must(t, os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("private-output\n"), 0600))
		git(t, repo, "add", ".")
		git(t, repo, "commit", "-m", "fixture")
		f.spec.Repositories = append(f.spec.Repositories, workspace.Repository{Name: name, Repository: repo})
	}
	f.source, err = f.store.InitializeWorkspaceContext(context.Background(), "source-create", f.spec)
	must(t, err)
	if f.source.Lease != nil || f.source.Workspace.Phase != "ready" {
		t.Fatal("fixture did not initialize")
	}
	for _, member := range f.source.Workspace.Members {
		must(t, os.WriteFile(filepath.Join(member.Path, "private-output"), []byte("source-only:"+member.Name), 0600))
	}
	f.m = workspacetransfer.Manager{Store: f.store}
	return f
}
func (f *fixture) row(key string) *model.Bench {
	f.t.Helper()
	a, err := f.store.WorkspaceRepair()
	must(f.t, err)
	var b *model.Bench
	must(f.t, a.Prep.Read(func(rows benchstate.Database) error { b = workspace.Clone(rows[key]); return nil }))
	return b
}
func (f *fixture) plan() *workspacetransfer.Plan {
	f.t.Helper()
	p, err := f.m.Inspect(context.Background(), "source", "target", "transfer1", 2<<20)
	must(f.t, err)
	return p
}
func (f *fixture) sql(fn func(*sql.Tx) error) error {
	db, err := statestore.Open(filepath.Join(f.store.State, "bench.db"), false)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
func (f *fixture) journal(id string) reftransfer.Record {
	f.t.Helper()
	var r reftransfer.Record
	must(f.t, f.sql(func(tx *sql.Tx) error { var err error; r, err = reftransfer.Get(tx, id); return err }))
	return r
}
func (f *fixture) stateBytes() []byte {
	f.t.Helper()
	var all []json.RawMessage
	must(f.t, f.sql(func(tx *sql.Tx) error {
		for _, table := range []string{"benches", "workspace_ref_transfers", "run_history", "setup_receipts"} {
			rows, err := tx.Query("SELECT * FROM " + table + " ORDER BY 1")
			if err != nil {
				return err
			}
			cols, err := rows.Columns()
			if err != nil {
				return err
			}
			for rows.Next() {
				v := make([]any, len(cols))
				dest := make([]any, len(v))
				for i := range v {
					dest[i] = &v[i]
				}
				if err := rows.Scan(dest...); err != nil {
					return err
				}
				raw, err := json.Marshal(v)
				if err != nil {
					return err
				}
				all = append(all, raw)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}))
	raw, err := json.Marshal(all)
	must(f.t, err)
	return raw
}
func read(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	must(t, err)
	return b
}
