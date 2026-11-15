package localprep

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/cachepolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/prepinput"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/setup"
)

// Prepare never releases its cooperative lease. The optional rerunOnce approval
// applies only to a prior unknown once hook; it does not attest that writers stopped.
func (m Manager) Prepare(ctx context.Context, key, token string, offline, rerunOnce bool) (*Record, error) {
	var result *Record
	if m.MaxLogBytes != 0 && (m.MaxLogBytes < 1024 || m.MaxLogBytes > 16<<20) {
		return nil, errors.New("cooperative log limit must be 1 KiB to 16 MiB")
	}
	e := m.gate(key, func() error {
		var record *Record
		var args, env []string
		var path string
		e := m.within(ctx, key, token, false, func(b *model.Bench) error {
			if e := m.inspect(ctx, b); e != nil {
				return e
			}
			old, e := read(m.State, key)
			if e != nil {
				return e
			}
			if old != nil && (old.Identity != identity(b) || !old.Acknowledged) {
				return ErrPending
			}
			config, inputs, e := prepinput.Read(b.Path)
			if e != nil {
				return e
			}
			if e = setup.MatchQuota(config, b.RequestedQuotaBytes); e != nil {
				return e
			}
			once := ""
			uncertain := false
			if old != nil {
				once = old.PreviousOnceSHA256
				uncertain = old.UncertainOnce
			}
			if uncertain && !rerunOnce {
				return errors.New("unknown cooperative once hook requires explicit rerun approval")
			}
			if uncertain || once != inputs {
				once = ""
			}
			if config != nil && config.Hooks() {
				args = config.Command(once == inputs, offline)
			} else {
				args, e = setup.FrozenCommandFor(b.Path, cachepolicy.Select(m.Data, b.Path, config.CacheSettings()), offline)
				if errors.Is(e, setup.ErrNoLockfile) {
					args = nil
				} else if e != nil {
					return e
				}
			}
			if config != nil && config.CacheSettings().Private() {
				tracked, e := m.git(ctx, b.Path, "ls-files", "--", ".bench/cache")
				if e != nil || tracked != "" {
					return errors.New("checkout-local cache namespace must be untracked")
				}
			}
			if e = m.admit(); e != nil {
				return e
			}
			if old != nil {
				raw, _ := json.Marshal(old)
				if e = preserveAcknowledged(m.State, old.Attempt, raw); e != nil {
					return e
				}
			}
			attempt, e := model.Nonce()
			if e != nil {
				return e
			}
			ranOnce := config != nil && strings.TrimSpace(config.Setup.Once) != "" && once != inputs
			record = &Record{RanOnce: ranOnce, Version: Version, Mode: "cooperative", Key: key, Identity: identity(b), OwnerSHA256: owner(b), Attempt: attempt, InputSHA256: inputs, StartedAt: time.Now().UTC().Format(time.RFC3339Nano), PreviousOnceSHA256: once, UncertainOnce: uncertain || config != nil && strings.TrimSpace(config.Setup.Once) != "" && once != inputs}
			if e = save(m.State, record); e != nil {
				return e
			}
			result = record
			path = b.Path
			paths, e := cachepolicy.Prepare(m.Data, b.Path, config.CacheSettings())
			if e != nil {
				return e
			}
			env = execution.EnvironmentFor(paths, os.Environ())
			return nil
		})
		if e != nil {
			return e
		}
		code := 0
		var runErr error
		var truncated bool
		if len(args) > 0 {
			log, e := os.OpenFile(filepath.Join(m.State, "local-preparation-"+record.Attempt+".log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e != nil {
				return e
			}
			limit := m.MaxLogBytes
			if limit == 0 {
				limit = 1 << 20
			}
			if limit < 1024 || limit > 16<<20 {
				log.Close()
				return errors.New("cooperative log limit must be 1 KiB to 16 MiB")
			}
			writer := &boundedWriter{file: log, left: limit}
			cmd := exec.CommandContext(ctx, args[0], args[1:]...)
			cmd.Dir = path
			cmd.Env = env
			cmd.Stdout = writer
			cmd.Stderr = writer
			cmd.WaitDelay = 2 * time.Second
			runErr = cmd.Run()
			code = -1
			if cmd.ProcessState != nil {
				code = cmd.ProcessState.ExitCode()
			}
			truncated = writer.truncated
			syncErr := log.Sync()
			closeErr := log.Close()
			if syncErr != nil || closeErr != nil || writer.err != nil {
				runErr = errors.Join(runErr, syncErr, closeErr, writer.err)
			}
		}
		saveErr := m.within(context.WithoutCancel(ctx), key, token, true, func(b *model.Bench) error {
			current, e := read(m.State, key)
			if e != nil {
				return e
			}
			if current == nil || current.Digest != record.Digest || current.Identity != identity(b) {
				return errors.New("cooperative attempt changed before observation")
			}
			current.Observed = runErr == nil && ctx.Err() == nil
			current.ExitCode = &code
			current.Truncated = truncated
			// Even a successful leader is only an observation. Descendant quiescence
			// requires the separate explicit operator acknowledgement.
			if e = save(m.State, current); e != nil {
				return e
			}
			result = current
			return nil
		})
		if runErr != nil {
			return errors.Join(errors.New("cooperative preparation did not complete; lease retained"), saveErr)
		}
		return saveErr
	})
	return result, e
}

type boundedWriter struct {
	truncated bool
	mu        sync.Mutex
	file      *os.File
	left      uint64
	err       error
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	keep := min(uint64(n), w.left)
	if keep < uint64(n) {
		w.truncated = true
	}
	if keep > 0 && w.err == nil {
		written, e := w.file.Write(p[:int(keep)])
		w.left -= uint64(written)
		w.err = e
		if e == nil && written != int(keep) {
			w.err = io.ErrShortWrite
		}
	}
	return n, nil
}
func (m Manager) git(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := gitcontrol.Command(ctx, dir, args...)
	var out identityOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if e := cmd.Run(); e != nil {
		return "", errors.New("cooperative checkout identity cannot be verified")
	}
	if out.overflow {
		return "", errors.New("cooperative Git identity exceeds bound")
	}
	return strings.TrimSpace(string(out.data)), nil
}
func (m Manager) inspect(ctx context.Context, b *model.Bench) error {
	canonicalPath, e := filepath.EvalSymlinks(b.Path)
	if e != nil {
		return e
	}
	for _, path := range []string{filepath.Join(m.Data, "mirrors"), b.Mirror} {
		info, e := os.Lstat(path)
		if e != nil || !info.IsDir() {
			return errors.New("physical cooperative mirror required")
		}
	}
	canonicalMirror, e := filepath.EvalSymlinks(b.Mirror)
	if e != nil {
		return e
	}
	for _, v := range []struct {
		args []string
		want string
	}{{[]string{"rev-parse", "--show-toplevel"}, canonicalPath}, {[]string{"symbolic-ref", "HEAD"}, "refs/heads/" + b.Branch}, {[]string{"config", "--get", "remote.origin.url"}, b.Repo}} {
		got, e := m.git(ctx, b.Path, v.args...)
		if e != nil || got != v.want {
			return errors.New("cooperative checkout identity changed")
		}
	}
	common, e := m.git(ctx, b.Path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if e != nil || common != canonicalMirror {
		return errors.New("cooperative mirror identity changed")
	}
	return nil
}

// Identity commands drain output within a fixed memory bound. Overflow is an
// observation failure, even if Git reports a successful exit.
type identityOutput struct {
	data     []byte
	overflow bool
}

func (o *identityOutput) Write(p []byte) (int, error) {
	n := len(p)
	keep := min(n, (16<<10)-len(o.data))
	o.data = append(o.data, p[:keep]...)
	if keep < n {
		o.overflow = true
	}
	return n, nil
}
