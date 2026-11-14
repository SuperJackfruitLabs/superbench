package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"os"
	"path/filepath"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: bench <create|recover|lease|checkpoint|release|status> [flags] [key]; flags precede key")
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
	token := f.String("token", os.Getenv("BENCH_LEASE_TOKEN"), "lease token; prefer BENCH_LEASE_TOKEN environment variable")
	if err = f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() > 1 {
		return fmt.Errorf("expected one key; flags must precede it")
	}
	key := f.Arg(0)
	if key == "" && args[0] != "status" {
		return fmt.Errorf("key required")
	}
	s := bench.Store{State: *state, Data: *data}
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
	case "status":
		out, err = s.Status(key)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}
