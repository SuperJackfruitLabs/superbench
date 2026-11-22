package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/client"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceapi"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceops"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacerenew"
)

func workspaceCLI(args []string, stdout, stderr io.Writer) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return workspaceCLIContext(ctx, args, stdout, stderr)
}

func workspaceCLIContext(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: bench workspace <create|recover|lease|setup|run|checkpoint|release|stop|stop_admin|renew_admin|archive|reclaim|status|host_status|plan-reap|reap|inspect-history|reconcile-history|inspect-initialization|claim-initialization|continue-initialization|inspect-transfer|begin-transfer|continue-transfer|inspect-missing-transfer|begin-missing-transfer|continue-missing-transfer|inspect-root-removal|reconcile-root-removal> [flags] [key]; flags precede key")
	}
	op := args[0]
	if op == "host-status" {
		op = "host_status"
	}
	if op == "stop-admin" || op == "renew-admin" {
		op = op[:len(op)-6] + "_admin"
	}
	repair := op == "inspect-initialization" || op == "claim-initialization" || op == "continue-initialization"
	missingTransfer := op == "inspect-missing-transfer" || op == "begin-missing-transfer" || op == "continue-missing-transfer"
	transfer := missingTransfer || op == "inspect-transfer" || op == "begin-transfer" || op == "continue-transfer"
	rootRemoval := op == "inspect-root-removal" || op == "reconcile-root-removal"
	localAdmin := op == "renew_admin" || rootRemoval || repair || transfer || op == "plan-reap" || op == "reap" || op == "inspect-history" || op == "reconcile-history"
	if !protocolv2.Known(op) && !localAdmin {
		return fmt.Errorf("unknown workspace operation %q", op)
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	f := flag.NewFlagSet("workspace "+op, flag.ContinueOnError)
	f.SetOutput(stderr)
	socket := f.String("socket", "", "benchd Unix socket; uses v2 only")
	state := f.String("state", filepath.Join(config, "superbench", "state"), "authoritative local state")
	data := f.String("data", filepath.Join(cache, "superbench", "data"), "local mirrors and checkouts")
	cgroup := f.String("cgroup-root", "/sys/fs/cgroup/superbench", "delegated local cgroup root")
	timeout := f.Duration("mirror-timeout", 2*time.Minute, "local managed Git deadline")
	id := f.String("request-id", "", "reuse this growth request ID after an interrupted reply")
	repositories := f.String("repositories", "", "file containing the canonical repositories JSON array")
	source := f.String("source", "", "file containing the exact recovery source JSON object")
	coordinator := f.String("checkpoint-repository", "", "logical checkpoint coordinator member")
	holder := f.String("holder", "", "workspace-wide lease holder")
	token := f.String("token", "", "holder token; ordinary operations accept BENCH_LEASE_TOKEN")
	member := f.String("repository-name", "", "selected member; omitted run uses root, setup uses all members")
	quota := f.Uint64("quota-bytes", 0, "one total workspace budget, bounded by host policy")
	initialize := f.Bool("initialize", true, "warm initialization is mandatory")
	checkpoint := f.Bool("checkpoint", true, "checkpoint and release a successful payload")
	offline := f.Bool("offline", false, "offline frozen setup")
	grace := f.Duration("archive-grace", 168*time.Hour, "local archive grace; zero explicitly waives retention")
	plan := f.String("plan", "", "concrete reviewed local administration manifest file")
	approve := f.String("approve", "", "exact reviewed plan Digest")
	repairID := f.String("repair-id", "", "explicit local initialization repair identity")
	rerunOnce := f.String("rerun-once", "", "review uncertain once hooks for these comma-separated members")
	target := f.String("target", "", "fresh destination key for reviewed local transfer")
	transferID := f.String("transfer-id", "", "explicit reviewed local transfer identity")
	dispositionID := f.String("disposition-id", "", "fresh identity for explicit current empty-directory removal")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() > 1 && op != "run" {
		return errors.New("expected one key; flags precede it")
	}
	key := f.Arg(0)
	visited := map[string]bool{}
	f.Visit(func(v *flag.Flag) { visited[v.Name] = true })
	store := bench.Store{State: *state, Data: *data, CgroupRoot: *cgroup, MirrorTimeout: *timeout}
	if localAdmin {
		allowed := map[string]bool{"state": true, "data": true, "cgroup-root": true, "mirror-timeout": true}
		if op == "reap" || op == "reconcile-history" {
			allowed["plan"], allowed["approve"] = true, true
		}
		if op == "inspect-root-removal" {
			allowed["disposition-id"] = true
		}
		if op == "reconcile-root-removal" {
			allowed["plan"], allowed["approve"] = true, true
		}
		if op == "inspect-initialization" {
			allowed["rerun-once"] = true
		}
		if op == "claim-initialization" {
			allowed["plan"], allowed["approve"], allowed["repair-id"] = true, true, true
		}
		if op == "continue-initialization" {
			allowed["repair-id"], allowed["token"] = true, true
		}
		if op == "inspect-transfer" || op == "inspect-missing-transfer" {
			allowed["target"], allowed["transfer-id"], allowed["quota-bytes"] = true, true, true
		}
		if op == "begin-transfer" || op == "begin-missing-transfer" {
			allowed["plan"], allowed["approve"] = true, true
		}
		if op == "continue-transfer" || op == "continue-missing-transfer" {
			allowed["transfer-id"] = true
		}
		for name := range visited {
			if !allowed[name] {
				return fmt.Errorf("flag --%s is not valid for local workspace %s", name, op)
			}
		}
		if rootRemoval {
			for _, name := range []string{"state", "data", "cgroup-root", "plan", "approve", "disposition-id"} {
				if visited[name] && f.Lookup(name).Value.String() == "" {
					return fmt.Errorf("flag --%s must not be empty", name)
				}
			}
			return workspaceRootRemoval(ctx, store, op, key, *dispositionID, *plan, *approve, stdout)
		}
		if transfer {
			for _, name := range []string{"state", "data", "cgroup-root", "plan", "approve", "target", "transfer-id"} {
				if visited[name] && f.Lookup(name).Value.String() == "" {
					return fmt.Errorf("flag --%s must not be empty", name)
				}
			}
			if missingTransfer {
				return workspaceMissingTransfer(ctx, store, op, key, *target, *transferID, *plan, *approve, *quota, stdout)
			}
			return workspaceTransfer(ctx, store, op, key, *target, *transferID, *plan, *approve, *quota, stdout)
		}
		if repair {
			for _, name := range []string{"state", "data", "cgroup-root", "plan", "approve", "repair-id", "token", "rerun-once"} {
				if visited[name] && f.Lookup(name).Value.String() == "" {
					return fmt.Errorf("flag --%s must not be empty", name)
				}
			}
			if op == "continue-initialization" && !visited["token"] {
				return errors.New("initialization repair continuation requires explicit --token")
			}
			return workspaceInitialization(ctx, store, op, key, *plan, *approve, *repairID, *token, *rerunOnce, stdout)
		}
		if op == "renew_admin" {
			return workspacerenew.Admin(store, key, stdout)
		}
		if op == "inspect-history" || op == "reconcile-history" {
			return workspaceHistory(ctx, store, op, key, *plan, *approve, stdout)
		}
		return workspaceReap(ctx, store, op, key, *plan, *approve, stdout)
	}
	if visited["disposition-id"] {
		return errors.New("root disposition flags are local administration only")
	}
	if visited["target"] || visited["transfer-id"] {
		return errors.New("workspace transfer flags are local administration only")
	}
	if visited["repair-id"] || visited["rerun-once"] {
		return errors.New("initialization repair flags are local administration only")
	}
	if visited["plan"] || visited["approve"] || visited["archive-grace"] && (op != "archive" || *socket != "") {
		return errors.New("reviewed plans and archive grace are local administration only")
	}
	// Flag usage prints defaults. Load ambient capabilities only after parsing
	// and local administration validation, and preserve explicit empty tokens.
	if !visited["token"] {
		*token = os.Getenv("BENCH_LEASE_TOKEN")
	}
	fields := map[string]any{"version": protocolv2.Version, "operation": op}
	if key != "" {
		fields["key"] = key
	}
	if *id != "" {
		fields["request_id"] = *id
	}
	if *repositories != "" {
		raw, err := workspaceReadFile(*repositories, protocolv2.MaxBody)
		if err != nil {
			return err
		}
		fields["repositories"] = json.RawMessage(raw)
	}
	if *source != "" {
		raw, err := workspaceReadFile(*source, protocolv2.MaxBody)
		if err != nil {
			return err
		}
		fields["source"] = json.RawMessage(raw)
	}
	if *coordinator != "" {
		fields["checkpoint_repository"] = *coordinator
	}
	if *holder != "" {
		fields["holder"] = *holder
	}
	if *member != "" {
		fields["repository_name"] = *member
	}
	if *token != "" && (op == "run" || op == "setup" || op == "checkpoint" || op == "release" || op == "stop" || visited["token"]) {
		fields["lease_token"] = *token
	}
	if visited["quota-bytes"] {
		fields["quota_bytes"] = *quota
	}
	if op == "create" || op == "recover" || visited["initialize"] {
		fields["initialize"] = *initialize
	}
	if visited["checkpoint"] {
		fields["checkpoint"] = *checkpoint
	}
	if visited["offline"] {
		fields["offline"] = *offline
	}
	if op == "run" {
		command := f.Args()[1:]
		if len(command) < 2 || command[0] != "--" {
			return errors.New("usage: bench workspace run [flags] key -- command [args]")
		}
		fields["arguments"] = command[1:]
	}
	if protocolv2.Growth(op) && *id == "" {
		nonce := make([]byte, 16)
		if _, err := rand.Read(nonce); err != nil {
			return err
		}
		*id = hex.EncodeToString(nonce)
		fields["request_id"] = *id
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	r, err := protocolv2.Parse(raw)
	if err != nil {
		return err
	}
	if r.RequestID != "" {
		if _, err := fmt.Fprintln(stderr, "request id:", r.RequestID); err != nil {
			return err
		}
	}
	var response protocolv2.Response
	if *socket != "" {
		response, err = client.CallV2(ctx, *socket, r)
	} else if op == "archive" && visited["archive-grace"] {
		if err := (workspaceops.Manager{Store: store}).Preflight(r); err != nil {
			return err
		}
		b, archiveErr := store.ArchiveWorkspaceContext(ctx, key, *grace, nil)
		out, proof, projectionErr := workspaceapi.Operation(r, &workspaceops.Result{Workspace: b}, archiveErr)
		response, _, err = workspaceapi.Envelope(r, out, proof, errors.Join(archiveErr, projectionErr))
		if err == nil {
			err = client.ErrorV2(response)
		}
	} else {
		response, _, err = workspaceapi.Execute(ctx, store, r, uint32(os.Getuid()))
		if err == nil {
			err = client.ErrorV2(response)
		}
	}
	if response.Version != "" {
		if writeErr := writeWorkspaceResponse(stdout, r, response); writeErr != nil {
			return writeErr
		}
	}
	return err
}

func writeWorkspaceResponse(stdout io.Writer, request protocolv2.Request, response protocolv2.Response) error {
	raw, err := json.Marshal(response)
	if err != nil {
		return err
	}
	limit := protocolv2.MaxResponse
	if protocolv2.Growth(request.Operation) {
		limit = protocolv2.MaxBody
	}
	if len(raw)+1 > limit {
		return errors.New("workspace response exceeds output limit; inspect retained state")
	}
	_, err = stdout.Write(append(raw, '\n'))
	return err
}
