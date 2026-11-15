package statestore

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSessionCommitsSurviveLaterRollbackAndEscapesClose(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	failed := errors.New("second phase failed")
	var escaped *Session
	calls := 0
	err := Within(state, func(canonical string) error {
		calls++
		want, e := filepath.EvalSymlinks(state)
		if e != nil || canonical != want {
			t.Fatal(canonical, want, e)
		}
		if _, e = os.Stat(filepath.Join(state, "bench.db")); !os.IsNotExist(e) {
			t.Fatal("before ran after database creation", e)
		}
		lock, e := os.OpenFile(filepath.Join(state, "lock"), os.O_RDWR, 0)
		if e != nil {
			t.Fatal(e)
		}
		defer lock.Close()
		if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e == nil {
			syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
			t.Fatal("before ran without host lock")
		}
		return nil
	}, func(s *Session) error {
		escaped = s
		if e := s.Update(func(tx *sql.Tx) error {
			_, e := tx.Exec("CREATE TABLE phases(value TEXT); INSERT INTO phases VALUES ('first')")
			return e
		}); e != nil {
			return e
		}
		return s.Update(func(tx *sql.Tx) error {
			if _, e := tx.Exec("INSERT INTO phases VALUES ('rolled-back')"); e != nil {
				return e
			}
			return failed
		})
	})
	if !errors.Is(err, failed) || calls != 1 {
		t.Fatal(err, calls)
	}
	if err = escaped.Update(func(*sql.Tx) error { t.Fatal("escaped session callback ran"); return nil }); !errors.Is(err, ErrSessionClosed) {
		t.Fatal(err)
	}
	assertFirst(t, state)
}

func assertFirst(t *testing.T, state string) {
	t.Helper()
	db, e := Open(filepath.Join(state, "bench.db"), true)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var count int
	var value string
	if e = db.QueryRow("SELECT count(*),min(value) FROM phases").Scan(&count, &value); e != nil || count != 1 || value != "first" {
		t.Fatal("durable phase differs", count, value, e)
	}
}

func TestSessionRejectsReentrantAndConcurrentUpdates(t *testing.T) {
	err := Within(t.TempDir(), nil, func(s *Session) error {
		return s.Update(func(*sql.Tx) error {
			if e := s.Update(func(*sql.Tx) error { return nil }); !errors.Is(e, ErrUpdateActive) {
				t.Fatal("nested update did not reject", e)
			}
			result := make(chan error, 1)
			go func() { result <- s.Update(func(*sql.Tx) error { return nil }) }()
			if e := <-result; !errors.Is(e, ErrUpdateActive) {
				t.Fatal("concurrent update did not reject", e)
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSessionAliasModesAndBeforeFailure(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	alias := filepath.Join(root, "alias")
	if e := os.Mkdir(state, 0750); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(state, alias); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(state, "lock"), nil, 0640); e != nil {
		t.Fatal(e)
	}
	want, _ := filepath.EvalSymlinks(state)
	failed := errors.New("recover refused")
	if e := Within(alias, func(got string) error {
		if got != want {
			t.Fatal(got, want)
		}
		return failed
	}, func(*Session) error { t.Fatal("callback after failed recovery"); return nil }); !errors.Is(e, failed) {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(state, "bench.db")); !os.IsNotExist(e) {
		t.Fatal("failed before created DB", e)
	}
	if e := Within(alias, nil, func(s *Session) error {
		return s.Update(func(tx *sql.Tx) error { _, e := tx.Exec("CREATE TABLE alias_test(value)"); return e })
	}); e != nil {
		t.Fatal(e)
	}
	for path, mode := range map[string]os.FileMode{state: 0750, filepath.Join(state, "lock"): 0640, filepath.Join(state, "bench.db"): 0600} {
		info, e := os.Stat(path)
		if e != nil || info.Mode().Perm() != mode {
			t.Fatal("existing modes changed", path, info, e)
		}
	}
}

// A subprocess uses stdin/stdout handshakes so lock exclusion is observed by a
// real nonblocking flock attempt, rather than inferred from elapsed time.
func TestSessionProcessHelper(t *testing.T) {
	mode := os.Getenv("SUPERBENCH_SESSION_HELPER")
	if mode == "" {
		return
	}
	state := os.Getenv("SUPERBENCH_SESSION_STATE")
	if mode == "competitor" || mode == "recover" {
		lock, e := os.OpenFile(filepath.Join(state, "lock"), os.O_RDWR, 0)
		if e != nil {
			t.Fatal(e)
		}
		e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if mode == "competitor" {
			if !errors.Is(e, syscall.EWOULDBLOCK) && !errors.Is(e, syscall.EAGAIN) {
				t.Fatal("competing writer entered commit gap", e)
			}
			fmt.Fprintln(os.Stdout, "blocked")
			var goAhead string
			if _, e = fmt.Fscanln(os.Stdin, &goAhead); e != nil {
				t.Fatal(e)
			}
		} else {
			if e != nil {
				t.Fatal("crash did not release OS lock", e)
			}
			syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		}
		lock.Close()
		if e = Within(state, nil, func(s *Session) error {
			return s.Update(func(tx *sql.Tx) error {
				var count int
				var value string
				e := tx.QueryRow("SELECT count(*),min(value) FROM phases").Scan(&count, &value)
				if e != nil || count != 1 || value != "first" {
					return fmt.Errorf("durable intent differs: %d %s %v", count, value, e)
				}
				return nil
			})
		}); e != nil {
			t.Fatal(e)
		}
		fmt.Fprintln(os.Stdout, "entered")
		return
	}
	failed := errors.New("second phase failed")
	e := Within(state, nil, func(s *Session) error {
		if e := s.Update(func(tx *sql.Tx) error {
			_, e := tx.Exec("CREATE TABLE phases(value TEXT); INSERT INTO phases VALUES ('first')")
			return e
		}); e != nil {
			return e
		}
		fmt.Fprintln(os.Stdout, "committed")
		var goAhead string
		if _, e := fmt.Fscanln(os.Stdin, &goAhead); e != nil {
			return e
		}
		return s.Update(func(tx *sql.Tx) error {
			if _, e := tx.Exec("INSERT INTO phases VALUES ('rolled-back')"); e != nil {
				return e
			}
			return failed
		})
	})
	if !errors.Is(e, failed) {
		t.Fatal(e)
	}
}

type sessionChild struct {
	cmd         *exec.Cmd
	in          io.WriteCloser
	out         *bufio.Reader
	diagnostics *bytes.Buffer
}

func startSessionChild(t *testing.T, state, mode string) *sessionChild {
	t.Helper()
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestSessionProcessHelper$")
	cmd.Env = append(os.Environ(), "SUPERBENCH_SESSION_HELPER="+mode, "SUPERBENCH_SESSION_STATE="+state)
	in, e := cmd.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	out, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	diagnostics := new(bytes.Buffer)
	cmd.Stderr = diagnostics
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return &sessionChild{cmd: cmd, in: in, out: bufio.NewReader(out), diagnostics: diagnostics}
}
func (c *sessionChild) expect(t *testing.T, want string) {
	t.Helper()
	got, e := c.out.ReadString('\n')
	if e != nil || strings.TrimSpace(got) != want {
		t.Fatal("child handshake differs", got, want, e)
	}
}
func (c *sessionChild) send(t *testing.T) {
	t.Helper()
	if _, e := fmt.Fprintln(c.in, "continue"); e != nil {
		t.Fatal(e)
	}
}
func (c *sessionChild) wait(t *testing.T) {
	t.Helper()
	if e := c.cmd.Wait(); e != nil {
		t.Fatal(e, c.diagnostics.String())
	}
}

func TestOSLockSpansCommitsAndSecondPhaseFailureAcrossProcesses(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	holder := startSessionChild(t, state, "holder")
	holder.expect(t, "committed")
	assertFirst(t, state)
	competitor := startSessionChild(t, state, "competitor")
	competitor.expect(t, "blocked")
	holder.send(t)
	holder.wait(t)
	competitor.send(t)
	competitor.expect(t, "entered")
	competitor.wait(t)
	assertFirst(t, state)
}

func TestCallerSIGKILLReleasesLockAndKeepsCommittedIntent(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	holder := startSessionChild(t, state, "holder")
	holder.expect(t, "committed")
	if e := holder.cmd.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	if e := holder.cmd.Wait(); e == nil {
		t.Fatal("SIGKILL helper unexpectedly succeeded")
	}
	recovery := startSessionChild(t, state, "recover")
	recovery.expect(t, "entered")
	recovery.wait(t)
	assertFirst(t, state)
}
