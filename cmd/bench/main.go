package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() > 0 {
			os.Exit(exit.ExitCode())
		}
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: bench <create|recover|lease|checkpoint|release|status|run|stop|migrate> [flags] [key]; flags precede key")
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
	state := f.String("state", filepath.Join(config, "superbench", "state"), "authoritative state, outside bench data")
	data := f.String("data", filepath.Join(cache, "superbench", "data"), "mirrors and checkouts")
	repo := f.String("repo", "", "git remote URL or absolute path")
	branch := f.String("branch", "", "task branch (default bench/<key>)")
	base := f.String("base", "main", "base branch")
	holder := f.String("holder", "", "lease holder")
	cgroupRoot := f.String("cgroup-root", "/sys/fs/cgroup/superbench", "delegated Linux cgroup-v2 root for fenced execution")
	checkpoint := f.Bool("checkpoint", true, "checkpoint and release a successful supervised command")
	admin := f.Bool("admin", false, "local operator: stop a supervised execution without its holder token")
	token := f.String("token", os.Getenv("BENCH_LEASE_TOKEN"), "lease token; prefer BENCH_LEASE_TOKEN environment variable")
	if err = f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() > 1 && args[0] != "run" {
		return fmt.Errorf("expected one key; flags must precede it")
	}
	key := f.Arg(0)
	if key == "" && args[0] != "status" && args[0] != "migrate" {
		return fmt.Errorf("key required")
	}
	s := bench.Store{State: *state, Data: *data, CgroupRoot: *cgroupRoot}
	var out any
	switch args[0] {
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
	case "status":
		out, err = s.Status(key)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
	if err != nil {
		if args[0] == "run" && out != nil {
			if e := json.NewEncoder(os.Stdout).Encode(out); e != nil {
				return e
			}
		}
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}
