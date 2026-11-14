package bench

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/retention"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

type Execution struct {
	ID            string
	Root          string
	BootID        string
	HostID        string
	Phase         string // starting, running, stopped
	PID           int
	Log           string
	LogLimitBytes uint64
}
type RunResult struct {
	Execution    *Execution
	Bench        *Bench
	ExitCode     int
	LogTruncated bool
}
type executionIdentity struct{ BootID, HostID string }
type executionBackend interface {
	Create(root, id string) (executionIdentity, error) // returns boot identity; never start outside scope
	Start(*exec.Cmd, *Execution) error
	Fence(*Execution) error // succeeds only after all scope descendants have stopped
	Empty(*Execution) error
}

func (s Store) backend() executionBackend {
	if s.executor != nil {
		return s.executor
	}
	return hostExecutor{}
}
func nonce() (string, error) {
	v := make([]byte, 16)
	if _, e := rand.Read(v); e != nil {
		return "", e
	}
	return hex.EncodeToString(v), nil
}
func newLease(holder string) (*Lease, error) {
	if holder == "" {
		return nil, errors.New("holder required")
	}
	token, e := nonce()
	if e != nil {
		return nil, e
	}
	return &Lease{Token: token, Holder: holder}, nil
}

// The execution gate linearizes start and stop independently from the global
// state lock. It is never held while waiting for a command's execution to finish.
func (s Store) executionGate(key string, fn func() error) error {
	if !keyPattern.MatchString(key) {
		return errors.New("invalid bench key")
	}
	f, e := os.OpenFile(filepath.Join(s.State, "execution-"+key+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); e != nil {
		return e
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}
func (s Store) Run(ctx context.Context, key, holder string, args []string, checkpoint bool) (*RunResult, error) {
	if len(args) == 0 {
		return nil, errors.New("command required")
	}
	if err := s.validate(); err != nil {
		return nil, err
	}
	id, err := nonce()
	if err != nil {
		return nil, err
	}
	root := s.CgroupRoot
	if root == "" {
		root = "/sys/fs/cgroup/superbench"
	}
	identity, err := s.backend().Create(root, id)
	if err != nil {
		return nil, err
	}
	execution := &Execution{ID: id, Root: root, BootID: identity.BootID, HostID: identity.HostID, Phase: "starting", Log: filepath.Join(s.State, "run-"+id+".log")}
	var b *Bench
	err = s.transactionSQL(func(db database, tx *sql.Tx) error {
		if e := retention.Directory(s.State); e != nil {
			return e
		}
		if e := s.admit(); e != nil {
			return e
		}
		original, e := get(db, key)
		if e != nil {
			return e
		}
		if original.ArchivedAt != "" {
			return ErrArchived
		}
		if original.Lease != nil {
			return ErrBusy
		}
		if e := s.ensureBenchQuota(original); e != nil {
			return e
		}
		limit, e := s.reserveLog(db)
		if e != nil {
			return e
		}
		execution.LogLimitBytes = limit
		lease, e := newLease(holder)
		if e != nil {
			return e
		}
		original.Lease = lease
		original.Lease.Execution = execution
		if e = history.Start(tx, history.Record{ID: id, Key: key, Holder: holder, StartedAt: time.Now().UTC()}); e != nil {
			return e
		}
		copy := *original
		b = &copy
		return nil
	})
	if err != nil {
		_ = s.backend().Fence(execution)
		return nil, err
	}
	result := &RunResult{Bench: b, ExitCode: -1, Execution: execution}
	capture, err := openLog(execution.Log, execution.LogLimitBytes)
	if err != nil {
		return result, err
	}
	defer capture.close()

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = b.Path
	cmd.Stdout = capture.writer
	cmd.Stderr = capture.writer
	cmd.Env = s.cacheEnvironment(os.Environ())
	// Persist intent before launch; stop and launch share this gate so a delayed
	// supervisor cannot create a new writer after another caller fenced the lease.
	err = s.startExecution(ctx, key, b, cmd, execution)
	_ = capture.writer.Close()
	if err != nil {
		if cmd.Process != nil {
			if fenceErr := s.backend().Fence(execution); fenceErr != nil {
				go func() { _ = cmd.Wait() }()
				return result, errors.Join(err, fmt.Errorf("execution fencing failed; lease retained: %w", fenceErr))
			}
			_ = cmd.Wait()
		}
		// Leave the durable starting lease held on any ambiguous save/launch failure.
		return result, fmt.Errorf("start failed; lease retained: %w", err)
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			// Kill the known scope even if state cannot be opened/written.
			_ = s.backend().Fence(execution)
			_, _ = s.Stop(key, b.Lease.Token, false)
		case <-done:
		}
	}()
	waitErr := cmd.Wait()
	close(done)
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	stopped, err := s.Stop(key, b.Lease.Token, false)
	if err != nil {
		return result, fmt.Errorf("unable to prove execution stopped; lease retained: %w", err)
	}
	result.Bench = stopped
	result.Execution = stopped.Lease.Execution
	logErr := capture.finish()
	result.LogTruncated = capture.truncated
	if logErr != nil {
		return result, fmt.Errorf("command log failed; stopped lease retained: %w", logErr)
	}
	if err = s.sealRunLog(id, capture, &result.ExitCode, result.LogTruncated); err != nil {
		return result, fmt.Errorf("log history save failed; stopped lease retained: %w", err)
	}
	if waitErr != nil {
		return result, fmt.Errorf("command failed; stopped lease retained: %w", waitErr)
	}
	if !checkpoint {
		return result, nil
	}
	cp, err := s.Checkpoint(key, b.Lease.Token)
	if err != nil {
		return result, fmt.Errorf("checkpoint failed; stopped lease retained: %w", err)
	}
	result.Bench.Checkpoint = cp.Checkpoint
	if err = s.Release(key, b.Lease.Token); err != nil {
		return result, err
	}
	result.Bench.Lease = nil
	return result, nil
}
func commandEnvironment(env []string) []string {
	var clean []string
	for _, v := range env {
		if len(v) >= 18 && v[:18] == "BENCH_LEASE_TOKEN=" {
			continue
		}
		clean = append(clean, v)
	}
	return clean
}

// Admin is for a local operator with access to the private state directory.
// Network adapters must authorize administrative access before calling it.
func (s Store) Stop(key, token string, admin bool) (*Bench, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	var result *Bench
	err := s.executionGate(key, func() error {
		var prior *Bench
		if err := s.read(func(db database) error {
			b, e := get(db, key)
			if e != nil {
				return e
			}
			if b.Lease == nil {
				return errors.New("bench is not leased")
			}
			if !admin {
				if e = owned(b, token); e != nil {
					return e
				}
			}
			if b.Lease.Execution == nil {
				return errors.New("lease has no supervised execution; stop its writer manually")
			}
			prior = b
			return nil
		}); err != nil {
			return err
		}
		if err := s.backend().Fence(prior.Lease.Execution); err != nil {
			return err
		}
		return s.transaction(func(db database) error {
			b, e := get(db, key)
			if e != nil {
				return e
			}
			if e = owned(b, prior.Lease.Token); e != nil {
				return e
			}
			if b.Lease.Execution == nil || b.Lease.Execution.ID != prior.Lease.Execution.ID {
				return errors.New("execution changed during stop")
			}
			b.Lease.Execution.Phase = "stopped"
			copy := *b
			result = &copy
			return nil
		})
	})
	return result, err
}

func (s Store) ensureStopped(b *Bench) error {
	if b.Lease == nil || b.Lease.Execution == nil {
		return nil
	}
	if b.Lease.Execution.Phase != "stopped" {
		return ErrNotFenced
	}
	return s.backend().Empty(b.Lease.Execution)
}

func (s Store) startExecution(ctx context.Context, key string, b *Bench, cmd *exec.Cmd, execution *Execution) error {
	return s.executionGate(key, func() error {
		return s.transaction(func(db database) error {
			current, e := get(db, key)
			if e != nil {
				return e
			}
			if e = owned(current, b.Lease.Token); e != nil {
				return e
			}
			if current.Lease.Execution == nil || current.Lease.Execution.ID != execution.ID || current.Lease.Execution.Phase != "starting" {
				return errors.New("execution was already stopped or replaced")
			}
			if e = ctx.Err(); e != nil {
				return e
			}
			if e = s.backend().Start(cmd, execution); e != nil {
				return e
			}
			current.Lease.Execution.Phase = "running"
			current.Lease.Execution.PID = cmd.Process.Pid
			return nil
		})
	})
}
