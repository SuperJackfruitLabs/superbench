package checkpointset

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type intent struct {
	Raw      json.RawMessage
	Digest   string
	Prepared string
}

func (c Coordinator) lock(ctx context.Context, bench string) (func(), error) {
	if !filepath.IsAbs(c.State) {
		return nil, errors.New("checkpoint state must be absolute")
	}
	if e := os.MkdirAll(c.State, 0700); e != nil {
		return nil, errors.New("checkpoint state unavailable")
	}
	info, e := os.Lstat(c.State)
	if e != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, errors.New("checkpoint state must be a private physical directory")
	}
	actual, e := filepath.EvalSymlinks(c.State)
	if e != nil || actual != filepath.Clean(c.State) {
		return nil, errors.New("checkpoint state must not traverse symlinks")
	}
	f, e := os.OpenFile(filepath.Join(c.State, bench+".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, errors.New("checkpoint lock unavailable")
	}
	for {
		e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if e == nil {
			break
		}
		if e != syscall.EWOULDBLOCK && e != syscall.EAGAIN {
			f.Close()
			return nil, errors.New("checkpoint lock failed")
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}
func (c Coordinator) journal(m Manifest, raw []byte) (intent, error) {
	var j intent
	path := filepath.Join(c.State, m.BenchID+"-"+m.GenerationID+".json")
	f, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if e == nil {
		defer f.Close()
		info, e := f.Stat()
		if e != nil || !info.Mode().IsRegular() || info.Size() > MaxRaw+4096 {
			return j, errors.New("invalid checkpoint intent")
		}
		data, e := io.ReadAll(io.LimitReader(f, MaxRaw+4097))
		if e != nil {
			return j, errors.New("checkpoint intent unavailable")
		}
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		if d.Decode(&j) != nil {
			return j, errors.New("invalid checkpoint intent")
		}
		if _, e = d.Token(); e != io.EOF {
			return j, errors.New("invalid checkpoint intent")
		}
		if !bytes.Equal(j.Raw, raw) || j.Digest != Digest(raw) {
			return j, errors.New("checkpoint generation intent differs")
		}
		if _, e = time.Parse(time.RFC3339, j.Prepared); e != nil {
			return j, errors.New("invalid checkpoint intent timestamp")
		}
		return j, nil
	}
	if !os.IsNotExist(e) {
		return j, errors.New("checkpoint intent unavailable")
	}
	j = intent{Raw: raw, Digest: Digest(raw), Prepared: time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)}
	data, e := json.Marshal(j)
	if e != nil {
		return j, e
	}
	tmp, e := os.CreateTemp(c.State, ".checkpoint-intent-")
	if e != nil {
		return j, errors.New("checkpoint intent unavailable")
	}
	defer os.Remove(tmp.Name())
	if e = tmp.Chmod(0600); e == nil {
		_, e = tmp.Write(data)
	}
	if e == nil {
		e = tmp.Sync()
	}
	closeErr := tmp.Close()
	if e == nil {
		e = closeErr
	}
	if e == nil {
		e = os.Rename(tmp.Name(), path)
	}
	if e == nil {
		dir, err := os.Open(c.State)
		if err != nil {
			e = err
		} else {
			e = dir.Sync()
			_ = dir.Close()
		}
	}
	if e != nil {
		return j, errors.New("checkpoint intent persistence failed")
	}
	return j, nil
}
