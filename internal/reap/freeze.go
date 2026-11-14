package reap

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"syscall"
)

type FreezePlan struct {
	Version, Key, Path, Checkpoint string
	Tree                           Tree
	Digest                         string
}

func (p FreezePlan) digest() (string, error) {
	p.Digest = ""
	raw, e := json.Marshal(p)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func NewFreeze(key, path, checkpoint string) (*FreezePlan, error) {
	tree, e := Snapshot(path, nil)
	if e != nil {
		return nil, e
	}
	if e = FreezePreflight(path, tree); e != nil {
		return nil, e
	}
	p := &FreezePlan{Version: "superbench.archive-freeze.v1", Key: key, Path: path, Checkpoint: checkpoint, Tree: tree}
	p.Digest, e = p.digest()
	return p, e
}

func (p FreezePlan) Bytes() ([]byte, error) {
	raw, e := json.Marshal(p)
	if e == nil && len(raw) > MaxManifest {
		e = errors.New("archive permission intent exceeds limit")
	}
	return raw, e
}

func ReadFreeze(path, key, checkout, checkpoint, digest string) (*FreezePlan, error) {
	f, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() || info.Size() > MaxManifest {
		return nil, errors.New("invalid archive permission intent")
	}
	raw, e := io.ReadAll(io.LimitReader(f, MaxManifest+1))
	if e != nil {
		return nil, e
	}
	if len(raw) > MaxManifest {
		return nil, errors.New("archive permission intent exceeds limit")
	}
	if e = CheckJSON(raw); e != nil {
		return nil, e
	}
	var p FreezePlan
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e = d.Decode(&p); e != nil {
		return nil, e
	}
	computed, e := p.digest()
	if e != nil {
		return nil, e
	}
	if p.Version != "superbench.archive-freeze.v1" || p.Key != key || p.Path != checkout || p.Checkpoint != checkpoint || p.Digest != digest || computed != digest {
		return nil, errors.New("archive permission intent conflicts with retained state")
	}
	return &p, nil
}
