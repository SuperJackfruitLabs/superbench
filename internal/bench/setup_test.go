package bench

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositorySetupReceiptsAndFailures(t *testing.T) {
	for _, mode := range []string{"success", "each-fails", "config-changes", "checkpoint-fails", "receipt-save-fails", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			s, remote := fixture(t)
			b, e := s.Create("task", Options{Repo: remote}, false)
			if e != nil {
				t.Fatal(e)
			}
			s.executor = &testExecutor{}
			if e = os.Mkdir(filepath.Join(b.Path, ".bench"), 0700); e != nil {
				t.Fatal(e)
			}
			config := "[setup]\nonce = 'printf once\\n >> .setup-events'\neach = 'printf each\\n >> .setup-events'\n"
			switch mode {
			case "each-fails":
				config = "[setup]\nonce='printf once >> .setup-events'\neach='exit 7'\n"
			case "config-changes":
				config = "[setup]\nonce='printf changed > .bench/config.toml'\n"
			case "checkpoint-fails":
				config = "[setup]\nonce='printf output > untracked-output'\n"
			case "invalid":
				config = "[setup]\nonse='touch .setup-events'\n"
			}
			write(t, filepath.Join(b.Path, ".bench/config.toml"), config)
			write(t, filepath.Join(b.Path, ".gitignore"), ".setup-events\n")
			g(t, b.Path, "add", ".")
			g(t, b.Path, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "setup")
			if mode == "receipt-save-fails" {
				if e = s.transactionSQL(func(_ database, tx *sql.Tx) error {
					_, e := tx.Exec("CREATE TRIGGER reject_setup BEFORE INSERT ON setup_receipts BEGIN SELECT RAISE(FAIL, 'injected receipt failure'); END")
					return e
				}); e != nil {
					t.Fatal(e)
				}
			}
			result, e := s.Setup(context.Background(), "task", "setup", false)
			if mode == "success" {
				if e != nil || result.Bench.Lease != nil {
					t.Fatal(result, e)
				}
				// A new Store instance must retain once completion in SQLite.
				s = Store{State: s.State, Data: s.Data, executor: &testExecutor{}}
				if _, e = s.Setup(context.Background(), "task", "next", true); e != nil {
					t.Fatal(e)
				}
				raw, e := os.ReadFile(filepath.Join(b.Path, ".setup-events"))
				if e != nil || strings.Count(string(raw), "once") != 1 || strings.Count(string(raw), "each") != 2 {
					t.Fatal(string(raw), e)
				}
				return
			}
			if e == nil {
				t.Fatal("unsafe setup succeeded")
			}
			states, e := s.Status("task")
			if e != nil {
				t.Fatal(e)
			}
			if mode == "invalid" {
				if states[0].Lease != nil {
					t.Fatal("invalid config acquired lease")
				}
			} else if states[0].Lease == nil || states[0].Lease.Execution.Phase != "stopped" {
				t.Fatal("failed setup did not retain stopped lease")
			}
			if e = s.readSQL(func(_ database, db reader) error {
				var count int
				if e := db.QueryRow("SELECT count(*) FROM setup_receipts").Scan(&count); e != nil {
					return e
				}
				want := 0
				if mode == "checkpoint-fails" {
					want = 1
				}
				if count != want {
					t.Fatal("incorrect setup completion receipt", count, want)
				}
				return nil
			}); e != nil {
				t.Fatal(e)
			}
		})
	}
}
