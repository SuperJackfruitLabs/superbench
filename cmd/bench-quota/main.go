// bench-quota exposes fixed-policy assignments, never arbitrary quota commands.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"os"
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
	if f.NArg() != 3 || f.Arg(0) != "apply" {
		return fmt.Errorf("usage: bench-quota [--policy ROOT_POLICY] apply <bench|shared> KEY")
	}
	p, e := hoststorage.Load(*policy)
	if e != nil {
		return e
	}
	assignment, e := p.Apply(f.Arg(1), f.Arg(2))
	if e != nil {
		return e
	}
	return json.NewEncoder(os.Stdout).Encode(assignment)
}
