package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/activation"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/buildinfo"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteaccess"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/service"
)

// newRemote builds remote access; it is called only when activation enables
// remote_access. Tests replace it to prove it is never called otherwise.
var newRemote = remoteaccess.New

func main() {
	if e := run(os.Args[1:], activation.Load, service.Serve); e != nil {
		fmt.Fprintln(os.Stderr, "benchd:", e)
		os.Exit(1)
	}
}

// run takes the activation loader and the server as parameters only so tests can
// prove that the loaded switches reach the server; main passes the real ones.
func run(args []string, load func() (activation.Switches, error), serve func(context.Context, service.Config) error) error {
	config, e := os.UserConfigDir()
	if e != nil {
		return e
	}
	cache, e := os.UserCacheDir()
	if e != nil {
		return e
	}
	if len(args) == 1 && args[0] == "version" {
		fmt.Println("benchd", buildinfo.Version)
		return nil
	}
	flags := flag.NewFlagSet("benchd", flag.ExitOnError)
	state := flags.String("state", filepath.Join(config, "superbench", "state"), "private authoritative state directory")
	data := flags.String("data", filepath.Join(cache, "superbench", "data"), "bench data directory")
	socket := flags.String("socket", "", "absolute Unix socket path (default state/benchd.sock)")
	cgroup := flags.String("cgroup-root", "/sys/fs/cgroup/superbench", "delegated cgroup-v2 root")
	operators := flags.String("operator-uids", strconv.Itoa(os.Geteuid())+",0", "comma-separated local operator uids")
	readers := flags.String("reader-uids", "", "comma-separated local read-only uids")
	retentionInterval := flags.Duration("retention-interval", time.Minute, "automatic retention check interval; inactive until policy is approved")
	archiveInterval := flags.Duration("archive-interval", time.Minute, "archive inspection interval; only committed branch-merged repository policy opts in")
	mirrorTimeout := flags.Duration("mirror-timeout", 2*time.Minute, "deadline for mirror clone/validation/fetch")
	active := flags.Int("max-active", 8, "maximum active operations")
	if e = flags.Parse(args); e != nil {
		return e
	}
	if flags.NArg() != 0 {
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
	// Activation comes only from the fixed root-owned file, never from flags or the environment.
	switches, e := load()
	if e != nil {
		return fmt.Errorf("%s: %w", activation.FixedPath, e)
	}
	log.Printf("activation: %s", switches)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	c := service.Config{Socket: *socket, Store: bench.Store{State: *state, Data: *data, CgroupRoot: *cgroup, MirrorTimeout: *mirrorTimeout}, Operators: ops, Readers: read, MaxActive: *active, RetentionInterval: *retentionInterval, ArchiveInterval: *archiveInterval, Activation: switches}
	if switches.RemoteAccess {
		remote, e := newRemote(c.Store, switches, ops, read)
		if e != nil {
			return e
		}
		c.Remote = remote
	}
	return serve(ctx, c)
}
