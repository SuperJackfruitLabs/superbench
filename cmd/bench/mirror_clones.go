package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/mirroradmin"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/mirrorstage"
)

func mirrorClonesCLI(args []string, stdout, stderr io.Writer) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return mirrorClonesContext(ctx, args, stdout, stderr)
}

func mirrorClonesContext(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: bench mirror-clones <inspect|collect|inspect-root-removal|reconcile-root-removal|forget> [flags] clone-id; flags precede id")
	}
	op := args[0]
	switch op {
	case "inspect", "collect", "inspect-root-removal", "reconcile-root-removal", "forget":
	default:
		return fmt.Errorf("unknown mirror-clones operation %q", op)
	}
	config, e := os.UserConfigDir()
	if e != nil {
		return e
	}
	cache, e := os.UserCacheDir()
	if e != nil {
		return e
	}
	f := flag.NewFlagSet("mirror-clones "+op, flag.ContinueOnError)
	f.SetOutput(stderr)
	state := f.String("state", filepath.Join(config, "superbench", "state"), "authoritative local state")
	data := f.String("data", filepath.Join(cache, "superbench", "data"), "local mirrors and checkouts")
	path := f.String("plan", "", "reviewed collection or empty-root disposition plan")
	approval := f.String("approve", "", "exact reviewed Digest")
	if e = f.Parse(args[1:]); e != nil {
		return e
	}
	if f.NArg() != 1 {
		return errors.New("one clone id required; flags precede it")
	}
	id := f.Arg(0)
	rawID, e := hex.DecodeString(id)
	if e != nil || len(rawID) != 16 || hex.EncodeToString(rawID) != id {
		return errors.New("clone id must be 32 lowercase hexadecimal characters")
	}
	visited := map[string]bool{}
	f.Visit(func(v *flag.Flag) { visited[v.Name] = true })
	for name := range visited {
		if f.Lookup(name).Value.String() == "" {
			return fmt.Errorf("flag --%s must not be empty", name)
		}
		if name == "plan" && op != "collect" && op != "reconcile-root-removal" || name == "approve" && op != "collect" && op != "reconcile-root-removal" && op != "forget" {
			return fmt.Errorf("flag --%s is not valid for mirror-clones %s", name, op)
		}
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	manager := mirroradmin.Manager(bench.Store{State: *state, Data: *data})
	switch op {
	case "inspect":
		p, e := manager.Inspect(ctx, id)
		if e != nil {
			return e
		}
		raw, e := p.Bytes()
		if e != nil {
			return e
		}
		_, e = stdout.Write(raw)
		return e
	case "inspect-root-removal":
		p, e := manager.InspectRootRemoval(ctx, id)
		if e != nil {
			return e
		}
		raw, e := p.Bytes()
		if e != nil {
			return e
		}
		_, e = stdout.Write(raw)
		return e
	case "forget":
		if *approval == "" {
			return errors.New("metadata retirement requires --approve with the completed collection Digest")
		}
		if e = manager.ForgetCompleted(ctx, id, *approval); e != nil {
			return e
		}
		return json.NewEncoder(stdout).Encode(struct {
			ID              string
			MetadataRetired bool
		}{id, true})
	}
	if *path == "" || *approval == "" {
		return errors.New("collection or root disposition requires --plan and --approve with the reviewed Digest")
	}
	limit := mirrorstage.MaxPlan
	if op == "reconcile-root-removal" {
		limit = mirrorstage.MaxRootRemovalReview
	}
	raw, e := workspaceReadFile(*path, limit)
	if e != nil {
		return e
	}
	var result *mirrorstage.Result
	if op == "collect" {
		p, err := mirrorstage.ParsePlan(raw)
		if err != nil {
			return err
		}
		if p.Intent.ID != id || p.Digest != *approval {
			return errors.New("approval must match the exact clone id and reviewed collection Digest")
		}
		result, e = manager.Apply(ctx, *p)
	} else {
		p, err := mirrorstage.ParseRootRemovalReview(raw)
		if err != nil {
			return err
		}
		if p.ID != id || p.Digest != *approval {
			return errors.New("approval must match the exact clone id and reviewed root disposition Digest")
		}
		result, e = manager.ReconcileRootRemoval(ctx, *p)
	}
	if e != nil {
		return e
	}
	return json.NewEncoder(stdout).Encode(result)
}
