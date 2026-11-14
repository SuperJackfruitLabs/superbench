package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/retention"
	"io"
	"os"
)

func approvedCollection(s bench.Store, path, approval string) (retention.Collection, error) {
	var zero retention.Collection
	if path == "" || approval == "" {
		return zero, errors.New("collection requires --plan and --approve matching the reviewed Digest")
	}
	f, e := os.Open(path)
	if e != nil {
		return zero, e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return zero, e
	}
	if !info.Mode().IsRegular() || info.Size() > reap.MaxManifest {
		return zero, errors.New("collection plan must be a bounded regular file")
	}
	raw, e := io.ReadAll(io.LimitReader(f, reap.MaxManifest+1))
	if e != nil {
		return zero, e
	}
	if e = reap.CheckJSON(raw); e != nil {
		return zero, e
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var p retention.Plan
	if e = d.Decode(&p); e != nil {
		return zero, e
	}
	if p.Digest != approval {
		return zero, errors.New("approval does not match the collection plan")
	}
	return s.Collect(p)
}

func approvedAdoption(s bench.Store, path, approval string, ack bool) (history.Record, error) {
	var zero history.Record
	if path == "" || approval == "" || !ack {
		return zero, errors.New("legacy adoption requires a reviewed --plan, matching --approve and --acknowledge-stopped-writers")
	}
	f, e := os.Open(path)
	if e != nil {
		return zero, e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return zero, e
	}
	if !info.Mode().IsRegular() || info.Size() > reap.MaxManifest {
		return zero, errors.New("adoption plan must be a bounded regular file")
	}
	raw, e := io.ReadAll(io.LimitReader(f, reap.MaxManifest+1))
	if e != nil {
		return zero, e
	}
	if e = reap.CheckJSON(raw); e != nil {
		return zero, e
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var p retention.Adoption
	if e = d.Decode(&p); e != nil {
		return zero, e
	}
	if p.Digest != approval {
		return zero, errors.New("approval does not match legacy log inspection")
	}
	return s.AdoptLog(p)
}
