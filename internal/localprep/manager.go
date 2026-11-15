package localprep

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/prepinput"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statepath"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
)

type Manager struct {
	State, Data string
	MaxLogBytes uint64
}

func (m Manager) within(ctx context.Context, key, token string, allowStopped bool, fn func(*model.Bench) error) error {
	if e := statepath.Validate(m.State, m.Data); e != nil {
		return e
	}
	if !keyPattern.MatchString(key) || token == "" {
		return errors.New("cooperative key and held token required")
	}
	// An existing bench command must initialize/migrate the state first. This
	// local path never silently upgrades an old live supervisor.
	if info, e := os.Lstat(filepath.Join(m.State, "bench.db")); e != nil || !info.Mode().IsRegular() {
		return errors.New("existing authoritative bench state required")
	}
	return statestore.Within(m.State, nil, func(s *statestore.Session) error {
		return s.Update(func(tx *sql.Tx) error {
			if e := ctx.Err(); e != nil {
				return e
			}
			if e := benchstate.CheckVersion(tx); e != nil {
				return e
			}
			rows, e := benchstate.Load(tx)
			if e != nil {
				return e
			}
			if e = benchstate.Validate(rows); e != nil {
				return e
			}
			b := rows[key]
			if b == nil || b.Workspace != nil || b.ArchivedAt != "" || b.Lease == nil || b.Lease.Token != token || b.Lease.Execution != nil || b.Lease.Continuation != "" || b.Lease.StopRequested && !allowStopped {
				return errors.New("current scalar cooperative owner required")
			}
			if b.Path != filepath.Join(m.Data, "benches", key) {
				return errors.New("cooperative checkout path mismatch")
			}
			canonicalData, e := filepath.EvalSymlinks(m.Data)
			if e != nil {
				return e
			}
			for _, p := range []string{m.Data, filepath.Join(m.Data, "benches"), b.Path} {
				info, e := os.Lstat(p)
				if e != nil || !info.IsDir() {
					return errors.New("physical cooperative checkout required")
				}
			}
			real, e := filepath.EvalSymlinks(b.Path)
			if e != nil || real != filepath.Join(canonicalData, "benches", key) {
				return errors.New("physical cooperative checkout required")
			}

			return fn(b)
		})
	})
}
func (m Manager) gate(key string, fn func() error) error {
	if err := statepath.Validate(m.State, m.Data); err != nil {
		return err
	}
	if !keyPattern.MatchString(key) {
		return errors.New("invalid bench key")
	}
	p := filepath.Join(m.State, "execution-"+key+".lock")
	f, e := os.OpenFile(p, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	// Local observation/ack never waits behind a still-running leader.
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return errors.New("cooperative preparation is still active")
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}
func (m Manager) Acknowledge(ctx context.Context, key, token, approved string, stopped bool) (*Record, error) {
	if !stopped || !hex64.MatchString(approved) {
		return nil, errors.New("exact preparation digest and explicit stopped-writer acknowledgement required")
	}
	var result *Record
	e := m.gate(key, func() error {
		return m.within(ctx, key, token, true, func(b *model.Bench) error {
			r, e := read(m.State, key)
			if e != nil {
				return e
			}
			if r == nil || r.Identity != identity(b) || r.OwnerSHA256 != owner(b) || r.Digest != approved && r.AcknowledgedDigest != approved {
				return errors.New("cooperative acknowledgement does not match held attempt")
			}
			if r.Acknowledged {
				result = r
				return nil
			}
			_, inputs, inputErr := prepinput.Read(b.Path)
			identityMatches := m.inspect(ctx, b) == nil
			if e = ctx.Err(); e != nil {
				return e
			}
			r.Successful = inputErr == nil && identityMatches && r.Observed && r.ExitCode != nil && *r.ExitCode == 0 && inputs == r.InputSHA256
			r.AcknowledgedDigest = approved
			r.Acknowledged = true
			if r.Successful {
				r.PreviousOnceSHA256 = r.InputSHA256
				if r.RanOnce {
					r.UncertainOnce = false
				}
			}
			if e = save(m.State, r); e != nil {
				return e
			}
			result = r
			return nil
		})
	})
	return result, e
}

// Status returns local observation metadata only; it never authorizes execution.
func (m Manager) Status(ctx context.Context, key, token string) (*Record, error) {
	var result *Record
	err := m.within(ctx, key, token, true, func(b *model.Bench) error {
		r, e := read(m.State, key)
		if e != nil {
			return e
		}
		if r == nil || r.Identity != identity(b) || r.OwnerSHA256 != owner(b) {
			return errors.New("no matching cooperative preparation")
		}
		result = r
		return nil
	})
	return result, err
}
