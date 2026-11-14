package bench

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const logMarker = "\n[SuperBench: log output truncated at configured limit]\n"

type logCapture struct {
	file, reader, writer *os.File
	done                 chan struct{}
	err                  error
	truncated            bool
}

func openLog(path string, limit uint64) (*logCapture, error) {
	if limit <= uint64(len(logMarker)) {
		return nil, errors.New("log limit is too small")
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return nil, e
	}
	dir, e := os.Open(filepath.Dir(path))
	if e != nil {
		f.Close()
		return nil, e
	}
	e = dir.Sync()
	dir.Close()
	if e != nil {
		f.Close()
		return nil, e
	}
	r, w, e := os.Pipe()
	if e != nil {
		f.Close()
		return nil, e
	}
	c := &logCapture{file: f, reader: r, writer: w, done: make(chan struct{})}
	go func() {
		defer close(c.done)
		buf := make([]byte, 32<<10)
		remaining := limit - uint64(len(logMarker))
		for {
			n, e := r.Read(buf)
			if n > 0 {
				keep := min(uint64(n), remaining)
				if keep > 0 && c.err == nil {
					written, err := f.Write(buf[:int(keep)])
					if err != nil {
						c.err = err
					} else if written != int(keep) {
						c.err = io.ErrShortWrite
					}
					remaining -= keep
				}
				if uint64(n) > keep && !c.truncated {
					c.truncated = true
					if c.err == nil {
						_, c.err = f.WriteString(logMarker)
					}
				}
			}
			if e != nil {
				if e != io.EOF && c.err == nil {
					c.err = e
				}
				return
			}
		}
	}()
	return c, nil
}
func (c *logCapture) finish() error {
	_ = c.writer.Close()
	<-c.done
	if c.err != nil {
		return c.err
	}
	return c.file.Sync()
}
func (c *logCapture) close() {
	_ = c.writer.Close()
	_ = c.reader.Close()
	<-c.done
	_ = c.file.Close()
}
func (s Store) logLimits() (per, total uint64, files int, e error) {
	per, total, files = 1<<20, 64<<20, 4096
	config, e := s.hostConfig()
	if e != nil {
		return 0, 0, 0, e
	}
	if config != nil {
		if config.MaxLogBytes != 0 {
			per = config.MaxLogBytes
		}
		if config.MaxLogsBytes != 0 {
			total = config.MaxLogsBytes
		}
		if config.MaxLogFiles != 0 {
			files = config.MaxLogFiles
		}
	}
	return
}
func (s Store) reserveLog(db database) (uint64, error) {
	per, total, maxFiles, e := s.logLimits()
	if e != nil {
		return 0, e
	}
	entries, e := os.ReadDir(s.State)
	if e != nil {
		return 0, e
	}
	sizes := map[string]uint64{}
	used := uint64(0)
	count := 0
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "run-") || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		info, e := entry.Info()
		if e != nil {
			return 0, e
		}
		if !info.Mode().IsRegular() {
			return 0, errors.New("run log is not a regular file")
		}
		size := uint64(info.Size())
		if size > total || used > total-size {
			return 0, errors.New("run log budget exhausted")
		}
		used += size
		count++
		sizes[filepath.Join(s.State, entry.Name())] = size
	}
	outstanding := uint64(0)
	for _, b := range db {
		if b.Lease != nil && b.Lease.Execution != nil {
			execution := b.Lease.Execution
			limit := execution.LogLimitBytes
			if limit == 0 {
				limit = per
			}
			size := sizes[execution.Log]
			if size < limit {
				remaining := limit - size
				if remaining > total || outstanding > total-remaining {
					return 0, errors.New("run log reservations exhausted")
				}
				outstanding += remaining
			}
		}
	}
	if count >= maxFiles || per > total || used > total-per || outstanding > total-per-used {
		return 0, errors.New("run log budget exhausted; review retention before starting more commands")
	}
	status, e := s.HostStatus()
	if e != nil {
		return 0, e
	}
	floor := uint64(1 << 20)
	if status.Config != nil {
		floor = status.Config.MinStateFreeBytes
	}
	if status.State.Free < floor || per+outstanding > status.State.Free-floor {
		return 0, errors.New("state free-space reserve cannot cover outstanding log allocations")
	}
	return per, nil
}
