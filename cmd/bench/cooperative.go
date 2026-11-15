package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/localprep"
)

func cooperativeCLI(operation string, args []string, out, stderr io.Writer) error {
	config, e := os.UserConfigDir()
	if e != nil {
		return e
	}
	cache, e := os.UserCacheDir()
	if e != nil {
		return e
	}
	f := flag.NewFlagSet(operation, flag.ContinueOnError)
	f.SetOutput(stderr)
	state := f.String("state", filepath.Join(config, "superbench", "state"), "existing authoritative state")
	data := f.String("data", filepath.Join(cache, "superbench", "data"), "registered bench data")
	token := f.String("token", "", "exact cooperative owner token; prefer environment")
	offline := f.Bool("offline", false, "offline frozen dependency setup")
	rerun := f.Bool("rerun-once", false, "approve rerunning an unknown cooperative once hook")
	digest := f.String("digest", "", "exact reviewed preparation Digest")
	stopped := f.Bool("acknowledge-stopped-writers", false, "operator confirms all cooperative writers stopped; not a supervised fence")
	limit := f.Uint64("max-log-bytes", 1<<20, "bounded local preparation output")
	if e = f.Parse(args); e != nil {
		return e
	}
	if f.NArg() != 1 {
		return errors.New("one registered bench key required; flags precede key")
	}
	allowed := map[string]bool{"state": true, "data": true, "token": true}
	switch operation {
	case "prepare-local":
		allowed["offline"] = true
		allowed["rerun-once"] = true
		allowed["max-log-bytes"] = true
	case "acknowledge-local-preparation":
		allowed["digest"] = true
		allowed["acknowledge-stopped-writers"] = true
	case "local-preparation-status":
	default:
		return errors.New("unknown local cooperative operation")
	}
	tokenProvided := false
	f.Visit(func(v *flag.Flag) {
		if v.Name == "token" {
			tokenProvided = true
		}
		if !allowed[v.Name] {
			e = errors.New("flag is not applicable to this local operation")
		}
	})
	if e != nil {
		return e
	}
	if !tokenProvided {
		*token = os.Getenv("BENCH_LEASE_TOKEN")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	m := localprep.Manager{State: *state, Data: *data, MaxLogBytes: *limit}
	var record *localprep.Record
	switch operation {
	case "prepare-local":
		record, e = m.Prepare(ctx, f.Arg(0), *token, *offline, *rerun)
	case "acknowledge-local-preparation":
		record, e = m.Acknowledge(ctx, f.Arg(0), *token, *digest, *stopped)
	case "local-preparation-status":
		record, e = m.Status(ctx, f.Arg(0), *token)
	}
	if record != nil {
		if encodeErr := json.NewEncoder(out).Encode(record); encodeErr != nil {
			return errors.Join(e, encodeErr)
		}
	}
	return e
}
