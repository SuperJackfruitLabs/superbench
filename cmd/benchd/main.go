package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/service"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "benchd:", e)
		os.Exit(1)
	}
}
func run() error {
	config, e := os.UserConfigDir()
	if e != nil {
		return e
	}
	cache, e := os.UserCacheDir()
	if e != nil {
		return e
	}
	state := flag.String("state", filepath.Join(config, "superbench", "state"), "private authoritative state directory")
	data := flag.String("data", filepath.Join(cache, "superbench", "data"), "bench data directory")
	socket := flag.String("socket", "", "absolute Unix socket path (default state/benchd.sock)")
	cgroup := flag.String("cgroup-root", "/sys/fs/cgroup/superbench", "delegated cgroup-v2 root")
	operators := flag.String("operator-uids", strconv.Itoa(os.Geteuid())+",0", "comma-separated local operator uids")
	readers := flag.String("reader-uids", "", "comma-separated local read-only uids")
	retentionInterval := flag.Duration("retention-interval", time.Minute, "automatic retention check interval; inactive until policy is approved")
	mirrorTimeout := flag.Duration("mirror-timeout", 2*time.Minute, "deadline for mirror clone/validation/fetch")
	active := flag.Int("max-active", 8, "maximum active operations")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if *socket == "" {
		*socket = filepath.Join(*state, "benchd.sock")
	}
	parse := func(input string) (map[uint32]bool, error) {
		uids := map[uint32]bool{}
		if input == "" {
			return uids, nil
		}
		for _, part := range strings.Split(input, ",") {
			uid, e := strconv.ParseUint(part, 10, 32)
			if e != nil {
				return nil, fmt.Errorf("invalid local uid")
			}
			uids[uint32(uid)] = true
		}
		return uids, nil
	}
	ops, e := parse(*operators)
	if e != nil {
		return e
	}
	read, e := parse(*readers)
	if e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return service.Serve(ctx, service.Config{Socket: *socket, Store: bench.Store{State: *state, Data: *data, CgroupRoot: *cgroup, MirrorTimeout: *mirrorTimeout}, Operators: ops, Readers: read, MaxActive: *active, RetentionInterval: *retentionInterval})
}
