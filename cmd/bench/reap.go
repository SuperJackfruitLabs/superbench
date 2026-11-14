package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
	"io"
	"os"
)

func approvedReap(s bench.Store, key, path, approval string) (*bench.Bench, error) {
	if path == "" || approval == "" {
		return nil, errors.New("reap requires --plan and --approve matching the reviewed Digest")
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() || info.Size() > reap.MaxManifest {
		return nil, errors.New("plan must be a bounded regular file")
	}
	raw, e := io.ReadAll(io.LimitReader(f, reap.MaxManifest+1))
	if e != nil {
		return nil, e
	}
	if e = reap.CheckJSON(raw); e != nil {
		return nil, e
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var p bench.ReapPlan
	if e = d.Decode(&p); e != nil {
		return nil, e
	}
	var trailing any
	if e = d.Decode(&trailing); e != io.EOF {
		return nil, errors.New("trailing plan data")
	}
	if p.Key != key || p.Digest != approval {
		return nil, errors.New("approval must match the exact plan and key")
	}
	return s.Reap(p)
}
