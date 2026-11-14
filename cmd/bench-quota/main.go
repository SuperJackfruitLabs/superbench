// bench-quota exposes fixed-policy assignments, never arbitrary quota commands.
package main

import (
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
	if !migrating && (f.NArg() != 3 || f.Arg(0) != "apply") && (f.NArg() != 4 || f.Arg(0) != "release" || f.Arg(1) != "bench") {
		return fmt.Errorf("usage: bench-quota [--policy ROOT_POLICY] apply <bench|shared> KEY | release bench KEY PROJECT_ID | migrate-inodes")
	}
	p, e := hoststorage.Load(*policy)
	if e != nil {
		return e
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
		assignment, e = p.Apply(f.Arg(1), f.Arg(2))
	}
	if e != nil {
		return e
	}
	return json.NewEncoder(os.Stdout).Encode(assignment)
}
