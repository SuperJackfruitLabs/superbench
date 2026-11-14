package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/maintenance"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
	"io"
	"os"
)

func approvedAutomaticPolicy(s bench.Store, path, digest string, ack bool) (maintenance.Status, error) {
	var zero maintenance.Status
	if path == "" || digest == "" || !ack {
		return zero, errors.New("automatic retention requires a reviewed --plan, matching --approve and --enable-automatic")
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
		return zero, errors.New("policy preview must be a bounded regular file")
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
	var p maintenance.Proposal
	if e = d.Decode(&p); e != nil {
		return zero, e
	}
	if p.Digest != digest {
		return zero, errors.New("approval does not match automatic policy preview")
	}
	return (maintenance.Manager{Root: s.State, Store: s}).Enable(p, ack)
}
