package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocol"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		var exit interface{ ExitCode() int }
		if errors.As(err, &exit) && exit.ExitCode() > 0 {
			os.Exit(exit.ExitCode())
		}
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: bench <inspect-log|adopt-log|history|reconcile-log|plan-collect|collect|resume-collect|cancel-collect|collections|archive|plan-reap|reap|create|recover|lease|checkpoint|release|status|run|stop|migrate|configure-host|host-status|setup> [flags] [key]; flags precede key")
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	limit := f.Int("limit", 100, "history/collection result limit")
	logAge := f.Duration("older-than", 168*time.Hour, "minimum time since release and log seal")
	metadataAge := f.Duration("metadata-older-than", 720*time.Hour, "minimum time since prior log collection")
	planFile := f.String("plan", "", "private reviewed administrative plan JSON")
	ackWriters := f.Bool("acknowledge-stopped-writers", false, "operator confirms legacy log writers were stopped")
	approval := f.String("approve", "", "exact Digest of the reviewed plan")
	socket := f.String("socket", "", "local benchd Unix socket (operation output uses the versioned API shape)")
	requestID := f.String("request-id", "", "stable id for replaying an accepted service request")
	state := f.String("state", filepath.Join(config, "superbench", "state"), "authoritative state, outside bench data")
	data := f.String("data", filepath.Join(cache, "superbench", "data"), "mirrors and checkouts")
	repo := f.String("repo", "", "git remote URL or absolute path")
	branch := f.String("branch", "", "task branch (default bench/<key>)")
	base := f.String("base", "main", "base branch")
	holder := f.String("holder", "", "lease holder")
	cgroupRoot := f.String("cgroup-root", "/sys/fs/cgroup/superbench", "delegated Linux cgroup-v2 root for fenced execution")
	checkpoint := f.Bool("checkpoint", true, "checkpoint and release a successful supervised command")
	admin := f.Bool("admin", false, "local operator: stop a supervised execution without its holder token")
	quotaHelper := f.String("quota-helper", "/usr/local/libexec/bench-quota", "fixed-policy quota helper")
	quotaPolicy := f.String("quota-policy", "", "policy override for direct root testing/administration")
	minData := f.Uint64("min-data-free", 64<<20, "minimum free bytes on the data filesystem")
	minState := f.Uint64("min-state-free", 1<<20, "minimum free bytes on the state filesystem")
	maxLog := f.Uint64("max-log-bytes", 1<<20, "maximum captured output bytes per command")
	maxLogs := f.Uint64("max-logs-bytes", 64<<20, "maximum retained and reserved run-log bytes")
	maxLogFiles := f.Int("max-log-files", 4096, "maximum retained run logs before admission stops")
	offline := f.Bool("offline", false, "offline frozen dependency setup")
	token := f.String("token", os.Getenv("BENCH_LEASE_TOKEN"), "lease token; prefer BENCH_LEASE_TOKEN environment variable")
	if err = f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() > 1 && args[0] != "run" {
		return fmt.Errorf("expected one key; flags must precede it")
	}
	key := f.Arg(0)
	if key == "" && args[0] != "status" && args[0] != "migrate" && args[0] != "configure-host" && args[0] != "host-status" && args[0] != "history" && args[0] != "plan-collect" && args[0] != "collect" && args[0] != "collections" && args[0] != "adopt-log" {
		return fmt.Errorf("key required")
	}
	switch args[0] {
	case "plan-collect", "collect", "collections", "adopt-log":
		if key != "" {
			return fmt.Errorf("this operation does not accept a key")
		}
	}
	if *socket != "" {
		r := protocol.Request{Operation: args[0], Key: key, RequestID: *requestID}
		switch args[0] {
		case "create", "recover":
			r.Repository = *repo
			r.Branch = *branch
			r.Base = *base
		case "lease":
			r.Holder = *holder
		case "checkpoint", "release":
			r.LeaseToken = *token
		case "stop":
			if *admin {
				r.Operation = "stop_admin"
			} else {
				r.LeaseToken = *token
			}
		case "status", "host-status":
			if args[0] == "host-status" {
				r.Operation = "host_status"
			}
		case "run":
			command := f.Args()[1:]
			if len(command) < 2 || command[0] != "--" {
				return fmt.Errorf("usage: bench run [flags] key -- command [args]")
			}
			r.Arguments = command[1:]
			r.Holder = *holder
			r.Checkpoint = checkpoint
		case "setup":
			r.Holder = *holder
			r.Offline = offline
		default:
			return fmt.Errorf("operation is local administration only")
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return remote(ctx, *socket, r)
	}
	s := bench.Store{State: *state, Data: *data, CgroupRoot: *cgroupRoot}
	var out any
	switch args[0] {
	case "inspect-log":
		out, err = s.InspectLog(key)
	case "adopt-log":
		out, err = approvedAdoption(s, *planFile, *approval, *ackWriters)
	case "history":
		out, err = s.History(key, *limit)
	case "reconcile-log":
		out, err = s.ReconcileLog(key)
	case "plan-collect":
		out, err = s.PlanCollection(*logAge, *metadataAge, *limit)
	case "collect":
		out, err = approvedCollection(s, *planFile, *approval)
	case "resume-collect", "cancel-collect":
		if *approval != key {
			return fmt.Errorf("approval must match the collection digest")
		}
		out, err = s.ResumeCollection(key, args[0] == "cancel-collect")
	case "collections":
		out, err = s.Collections()
	case "archive":
		out, err = s.Archive(key)
	case "plan-reap":
		out, err = s.PlanReap(key)
	case "reap":
		out, err = approvedReap(s, key, *planFile, *approval)
	case "create", "recover":
		out, err = s.Create(key, bench.Options{Repo: *repo, Branch: *branch, Base: *base}, args[0] == "recover")
	case "lease":
		out, err = s.Lease(key, *holder)
	case "checkpoint":
		out, err = s.Checkpoint(key, *token)
	case "release":
		err = s.Release(key, *token)
		out = map[string]string{"released": key}
	case "run":
		command := f.Args()[1:]
		if len(command) < 2 || command[0] != "--" {
			return fmt.Errorf("usage: bench run [flags] key -- command [args]")
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		out, err = s.Run(ctx, key, *holder, command[1:], *checkpoint)
	case "stop":
		out, err = s.Stop(key, *token, *admin)
	case "migrate":
		err = s.Migrate()
		out = map[string]string{"migration": "complete"}
	case "configure-host":
		cfg := bench.HostConfig{DataRoot: *data, QuotaHelper: *quotaHelper, PolicyOverride: *quotaPolicy, MinDataFreeBytes: *minData, MinStateFreeBytes: *minState, MaxLogBytes: *maxLog, MaxLogsBytes: *maxLogs, MaxLogFiles: *maxLogFiles}
		err = s.ConfigureHost(cfg)
		out = cfg
	case "host-status":
		out, err = s.HostStatus()
	case "setup":
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		out, err = s.Setup(ctx, key, *holder, *offline)
	case "status":
		out, err = s.Status(key)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
	if err != nil {
		if (args[0] == "run" || args[0] == "setup") && out != nil {
			if e := json.NewEncoder(os.Stdout).Encode(out); e != nil {
				return e
			}
		}
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}
