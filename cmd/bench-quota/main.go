// The runtime otherwise keeps the cgroup cpu.max file open to update
// GOMAXPROCS, and the clean worker's descriptor check would refuse it.
//go:debug containermaxprocs=0

// bench-quota exposes fixed-policy assignments, never arbitrary quota commands.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/cleanworker"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"io"
	"os"
	"strconv"
)

func main() {
	// Root re-executes this image as the daemon-uid clean worker. It is
	// dispatched before any flag or policy parsing, and does its own checks.
	if len(os.Args) > 1 && os.Args[1] == cleanworker.Command {
		os.Exit(cleanworker.Main(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
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
	if f.NArg() > 0 && (f.Arg(0) == "initialize-indexed" || f.Arg(0) == "bootstrap-payload" || f.Arg(0) == "inspect-bootstrap-payload" || f.Arg(0) == "plan-retire-bootstrap" || f.Arg(0) == "retire-bootstrap" || isRecoveryCommand(f.Arg(0))) {
		return runBootstrapCommand(f.Args(), *policy != "/etc/superbench/storage.json", os.Stdin, os.Stdout)
	}
	if f.NArg() > 0 && isOperationCommand(f.Arg(0)) {
		request, err := parseOperation(f.Args())
		if err != nil {
			return err
		}
		p, err := hoststorage.Load(*policy)
		if err != nil {
			return err
		}
		return runOperation(context.Background(), p, request, os.Stdout)
	}
	migrating := f.NArg() == 1 && f.Arg(0) == "migrate-inodes"
	retirement := f.NArg() == 3 && (f.Arg(0) == "retire-approved" || f.Arg(0) == "verify-retired" || f.Arg(0) == "verify-retirement-authority") && f.Arg(1) == "bench"
	allocation := f.NArg() == 3 && f.Arg(0) == "inspect-allocation" && f.Arg(1) == "bench"
	verifying := f.NArg() == 3 && f.Arg(0) == "verify" && f.Arg(1) == "bench"
	if !allocation && !migrating && !verifying && !retirement && ((f.NArg() != 3 && f.NArg() != 4) || f.Arg(0) != "apply") && (f.NArg() != 4 || f.Arg(0) != "release" || f.Arg(1) != "bench") {
		return fmt.Errorf("usage: bench-quota [--policy ROOT_POLICY] apply <bench|shared> KEY [BENCH_BYTES] | verify bench KEY | inspect-allocation bench KEY | release bench KEY PROJECT_ID | retire-approved bench KEY | verify-retired bench KEY | verify-retirement-authority bench KEY | migrate-inodes")
	}
	p, e := hoststorage.Load(*policy)
	if e != nil {
		return e
	}
	if migrating && p.OperationJournal != "" {
		return fmt.Errorf("indexed operation policy does not permit inode migration")
	}
	if err := checkIndexedCommand(p, f.Arg(0)); err != nil {
		return err
	}
	var approval hoststorage.RetirementApproval
	if retirement {
		var err error
		// Complete bounded input validation before acquiring operations.lock.
		approval, err = readRetirementApproval(f.Arg(2), os.Stdin)
		if err != nil {
			return err
		}
	}
	return runFixedPolicy(context.Background(), *policy, p, func(output io.Writer) error {
		if retirement {
			return runRetirementApproval(p, f.Arg(0), approval, output)
		}
		if allocation {
			observation, err := p.InspectAllocation(context.Background(), f.Arg(2))
			if err != nil {
				return err
			}
			raw, err := json.Marshal(observation)
			if err != nil {
				return err
			}
			if len(raw) > hoststorage.MaxStorageIdentity {
				return fmt.Errorf("allocation observation exceeds limit")
			}
			_, err = output.Write(raw)
			return err
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
			_, err = output.Write(raw)
			return err
		}
		if migrating {
			assignments, e := p.MigrateInodes()
			if e != nil {
				return e
			}
			return json.NewEncoder(output).Encode(assignments)
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
		return json.NewEncoder(output).Encode(assignment)
	}, os.Stdout)
}
