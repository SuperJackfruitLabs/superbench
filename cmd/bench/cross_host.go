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
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/crosshost"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/crosshostflow"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/handoffjournal"
)

func handoffCLI(args []string, out, stderr io.Writer) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return handoffCLIContext(ctx, args, out, stderr)
}
func handoffCLIContext(ctx context.Context, args []string, out, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: bench handoff <offer|inspect-export|begin-export|continue-export|import|continue-import> [flags] key; flags precede key")
	}
	op := args[0]
	allowed := map[string]bool{"state": true, "data": true, "mirror-timeout": true, "cgroup-root": true}
	switch op {
	case "offer":
		allowed["creation-id"] = true
		allowed["quota-bytes"] = true
	case "inspect-export":
		allowed["offer"] = true
		allowed["transfer-id"] = true
	case "begin-export":
		allowed["plan"] = true
		allowed["approve"] = true
	case "continue-export":
		allowed["transfer-id"] = true
	case "import":
		allowed["artifact"] = true
		allowed["approve"] = true
	case "continue-import":
		allowed["creation-id"] = true
	default:
		return errors.New("unknown local handoff operation")
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	f := flag.NewFlagSet(op, flag.ContinueOnError)
	f.SetOutput(stderr)
	state := f.String("state", filepath.Join(config, "superbench", "state"), "authoritative local state")
	data := f.String("data", filepath.Join(cache, "superbench", "data"), "local mirrors and workspaces")
	timeout := f.Duration("mirror-timeout", 2*time.Minute, "bounded Git operation timeout")

	cgroup := f.String("cgroup-root", "/sys/fs/cgroup/superbench", "delegated Linux execution root")
	creation := f.String("creation-id", "", "immutable destination creation identity")
	id := f.String("transfer-id", "", "immutable source export identity")
	quota := f.Uint64("quota-bytes", 0, "explicit destination quota; zero derives repository declarations")
	offerFile := f.String("offer", "", "private actual-host destination offer")
	planFile := f.String("plan", "", "exact private reviewed export approval")
	artifactFile := f.String("artifact", "", "private final export artifact")
	approve := f.String("approve", "", "exact reviewed Digest")
	if err = f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 1 {
		return errors.New("one registered key required; flags precede key")
	}
	f.Visit(func(v *flag.Flag) {
		if !allowed[v.Name] {
			err = errors.New("flag is not applicable to this local handoff operation")
		}
	})
	if err != nil {
		return err
	}
	m := crosshostflow.Manager{Store: bench.Store{State: *state, Data: *data, MirrorTimeout: *timeout, CgroupRoot: *cgroup}}
	key := f.Arg(0)
	var value any
	switch op {
	case "offer":
		if *creation == "" {
			return errors.New("offer requires --creation-id")
		}
		var o *handoffjournal.Offer
		o, err = m.Offer(ctx, key, *creation, *quota)
		value = o
	case "inspect-export":
		if *offerFile == "" || *id == "" {
			return errors.New("inspection requires --offer and --transfer-id")
		}
		raw, e := workspaceReadFile(*offerFile, 4096)
		if e != nil {
			return e
		}
		o, e := crosshostflow.ParseOffer(raw)
		if e != nil {
			return e
		}
		var p *crosshostflow.Plan
		p, err = m.InspectExport(ctx, key, *id, o)
		value = p
	case "begin-export":
		if *planFile == "" || *approve == "" {
			return errors.New("begin requires --plan and exact --approve")
		}
		raw, e := workspaceReadFile(*planFile, handoffjournal.MaxApproval)
		if e != nil {
			return e
		}
		p, e := crosshostflow.ParsePlan(raw)
		if e != nil {
			return e
		}
		if p.SourceKey != key || p.Digest != *approve {
			return errors.New("reviewed approval/source mismatch")
		}
		value, err = m.BeginExport(ctx, p)
	case "continue-export":
		if *id == "" {
			return errors.New("continuation requires --transfer-id")
		}
		value, err = m.ContinueExport(ctx, key, *id)
	case "import":
		if *artifactFile == "" || *approve == "" {
			return errors.New("import requires --artifact and exact --approve")
		}
		raw, e := workspaceReadFile(*artifactFile, crosshost.MaxArtifact)
		if e != nil {
			return e
		}
		a, e := crosshost.Parse(raw)
		if e != nil {
			return e
		}
		if a.Destination.Key != key || a.Digest != *approve {
			return errors.New("reviewed artifact/destination mismatch")
		}
		value, err = m.Import(ctx, a)
	case "continue-import":
		if *creation == "" {
			return errors.New("continuation requires --creation-id")
		}
		value, err = m.ContinueImport(ctx, key, *creation)
	}
	// A typed nil result is not retained evidence and must emit no JSON.
	switch v := value.(type) {
	case *handoffjournal.Offer:
		if v == nil {
			return err
		}
	case *crosshostflow.Plan:
		if v == nil {
			return err
		}
	case *crosshost.Artifact:
		if v == nil {
			return err
		}
	case *crosshostflow.Result:
		if v == nil {
			return err
		}
	}
	if value != nil {
		raw, e := json.Marshal(value)
		if e != nil {
			return errors.Join(err, e)
		}
		limit := handoffjournal.MaxApproval
		if op == "continue-export" {
			limit = crosshost.MaxArtifact
		}
		if len(raw)+1 > limit {
			return errors.Join(err, errors.New("handoff output exceeds bound; inspect retained state"))
		}
		_, e = out.Write(append(raw, '\n'))
		err = errors.Join(err, e)
	}
	return err
}
