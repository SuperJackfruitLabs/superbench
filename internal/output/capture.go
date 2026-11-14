// Package output captures bounded output and checks aggregate log admission.
package output

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

const Marker = "\n[SuperBench: log output Truncated at configured limit]\n"

type Capture struct {
	File, reader, Writer *os.File
	done                 chan struct{}
	err                  error
	Truncated            bool
}

func Open(path string, limit uint64) (*Capture, error) {
	if limit <= uint64(len(Marker)) {
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
	c := &Capture{File: f, reader: r, Writer: w, done: make(chan struct{})}
	go func() {
		defer close(c.done)
		buf := make([]byte, 32<<10)
		remaining := limit - uint64(len(Marker))
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
				if uint64(n) > keep && !c.Truncated {
					c.Truncated = true
					if c.err == nil {
						_, c.err = f.WriteString(Marker)
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
func (c *Capture) Finish() error {
	_ = c.Writer.Close()
	<-c.done
	if c.err != nil {
		return c.err
	}
	return c.File.Sync()
}
func (c *Capture) Close() {
	_ = c.Writer.Close()
	_ = c.reader.Close()
	<-c.done
	_ = c.File.Close()
}
