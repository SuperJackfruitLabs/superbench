// bench-quota exposes fixed-policy assignments, never arbitrary quota commands.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"os"
	"strconv"
)

func main() {
	if e := run(os.Args[1:]); e != nil {
		fmt.Fprintln(os.Stderr, "bench-quota:", e)
		os.Exit(1)
	}
}
func run(args []string) error {
	f := flag.NewFlagSet("bench-quota", flag.ContinueOnError)
	policy := f.String("policy", "/etc/superbench/storage.json", "root-owned policy (override only for direct root administration)")
	if e := f.Parse(args); e != nil {
		return e
	}
	if *policy != "/etc/superbench/storage.json" && (os.Getuid() != 0 || os.Geteuid() != 0 || os.Getenv("SUDO_UID") != "") {
		return fmt.Errorf("policy override requires a direct root caller")
	}
	migrating := f.NArg() == 1 && f.Arg(0) == "migrate-inodes"
	verifying := f.NArg() == 3 && f.Arg(0) == "verify" && f.Arg(1) == "bench"
	if !migrating && !verifying && ((f.NArg() != 3 && f.NArg() != 4) || f.Arg(0) != "apply") && (f.NArg() != 4 || f.Arg(0) != "release" || f.Arg(1) != "bench") {
		return fmt.Errorf("usage: bench-quota [--policy ROOT_POLICY] apply <bench|shared> KEY [BENCH_BYTES] | verify bench KEY | release bench KEY PROJECT_ID | migrate-inodes")
	}
	p, e := hoststorage.Load(*policy)
	if e != nil {
		return e
	}
	if verifying {
		identity, err := p.VerifyStorage(context.Background(), f.Arg(2))
		if err != nil {
			return err
		}
		raw, err := json.Marshal(identity)
		if err != nil {
			return err
		}
		if len(raw) > hoststorage.MaxStorageIdentity {
			return fmt.Errorf("storage identity exceeds limit")
		}
		_, err = os.Stdout.Write(raw)
		return err
	}
	if migrating {
		assignments, e := p.MigrateInodes()
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(assignments)
	}
	var assignment hoststorage.Assignment
	if f.Arg(0) == "release" {
		id, parseErr := strconv.ParseUint(f.Arg(3), 10, 32)
		if parseErr != nil {
			return parseErr
		}
		assignment, e = p.Release(f.Arg(2), uint32(id))
	} else {
		requested := uint64(0)
		if f.NArg() == 4 {
			if f.Arg(1) != "bench" {
				return fmt.Errorf("only bench allocations accept a size request")
			}
			requested, e = strconv.ParseUint(f.Arg(3), 10, 64)
			if e != nil || requested == 0 {
				return fmt.Errorf("invalid requested bench bytes")
			}
		}
		assignment, e = p.ApplyRequested(f.Arg(1), f.Arg(2), requested)
	}
	if e != nil {
		return e
	}
	return json.NewEncoder(os.Stdout).Encode(assignment)
}
