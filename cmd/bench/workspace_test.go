package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/client"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacearchive"
)

func cliSocket(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sb-cli-")
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "socket")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	s := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	go func() { _ = s.Serve(l) }()
	t.Cleanup(func() { _ = s.Close(); _ = os.RemoveAll(dir) })
	return socket
}

func cliFixtureBody(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../internal/protocolv2/testdata/workspace-responses.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Valid []struct {
			Name string
			Body json.RawMessage
		}
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	for _, c := range corpus.Valid {
		if c.Name == name {
			return c.Body
		}
	}
	t.Fatal("fixture missing", name)
	return nil
}

func TestWorkspaceRemoteRunPreservesFailureGrantExitAndExactRequestID(t *testing.T) {
	t.Setenv("BENCH_LEASE_TOKEN", "")
	body := cliFixtureBody(t, "run-held-grant")
	var requests []protocolv2.Request
	socket := cliSocket(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/operations" {
			t.Error("CLI fell back to other protocol")
		}
		raw, _ := io.ReadAll(r.Body)
		request, err := protocolv2.Parse(raw)
		if err != nil {
			t.Error(err)
		}
		requests = append(requests, request)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(422)
		_, _ = w.Write(body)
	})
	args := []string{"run", "--socket", socket, "--request-id", "run-1", "--holder", "run:one", "--checkpoint=false", "--repository-name", "a", "card", "--", "/bin/false"}
	for i := 0; i < 2; i++ {
		var out, stderr bytes.Buffer
		err := workspaceCLIContext(context.Background(), args, &out, &stderr)
		var failed *client.WorkspaceError
		if !errors.As(err, &failed) || failed.ExitCode() != 7 {
			t.Fatal("CLI lost command failure exit", err)
		}
		response, err := protocolv2.ParseResponse(out.Bytes(), 422, requests[i])
		if err != nil {
			t.Fatal("failure result not preserved", err)
		}
		if response.Result.(*protocolv2.RunOutcome).Grant == nil || !strings.Contains(stderr.String(), "request id: run-1") {
			t.Fatal("authorized failure grant or request id omitted")
		}
	}
	if len(requests) != 2 || !reflect.DeepEqual(requests[0], requests[1]) || requests[0].RepositoryName != "a" {
		t.Fatal("explicit ID/body changed or client retried")
	}
}

func TestWorkspaceRemoteGeneratedIDAndPreEffectInputRefusals(t *testing.T) {
	t.Setenv("BENCH_LEASE_TOKEN", "")
	dir := t.TempDir()
	members := filepath.Join(dir, "members.json")
	if err := os.WriteFile(members, []byte(`[{"name":"api","repository":"/fixture/api.git"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	var request protocolv2.Request
	calls := 0
	socket := cliSocket(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		var err error
		request, err = protocolv2.Parse(raw)
		if err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(409)
		_ = json.NewEncoder(w).Encode(protocolv2.Response{Version: protocolv2.Version, Operation: request.Operation, RequestID: request.RequestID, Error: &protocolv2.APIError{Code: "conflict", Message: "Refused."}})
	})
	var out, stderr bytes.Buffer
	err := workspaceCLIContext(context.Background(), []string{"create", "--socket", socket, "--repositories", members, "--checkpoint-repository", "api", "card"}, &out, &stderr)
	if err == nil || len(request.RequestID) != 32 || !strings.Contains(stderr.String(), request.RequestID) || request.Initialize == nil || !*request.Initialize {
		t.Fatal("generated identity or mandatory warm init lost", err)
	}
	for _, args := range [][]string{
		{"archive", "--socket", socket, "--archive-grace", "0", "card"},
		{"reap", "--socket", socket, "--plan", "anything", "--approve", "anything", "card"},
		{"run", "--socket", socket, "--holder", "worker", "card", "/bin/false"},
	} {
		out.Reset()
		stderr.Reset()
		if err := workspaceCLIContext(context.Background(), args, &out, &stderr); err == nil || out.Len() != 0 {
			t.Fatal("invalid CLI request reached effects")
		}
	}
	if err := os.WriteFile(members, []byte(`[{"name":"api","name":"web","repository":"/fixture/api.git"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := workspaceCLIContext(context.Background(), []string{"create", "--socket", socket, "--repositories", members, "--checkpoint-repository", "api", "card"}, &out, &stderr); err == nil {
		t.Fatal("ambiguous repositories accepted")
	}
	if calls != 1 {
		t.Fatal("input refusal sent request", calls)
	}
}

func workspaceFixture(t *testing.T) (bench.Store, string) {
	t.Helper()
	t.Setenv("BENCH_LEASE_TOKEN", "")
	root := t.TempDir()
	store := bench.Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data")}
	t.Cleanup(func() {
		_ = filepath.WalkDir(store.Data, func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.Type()&os.ModeSymlink == 0 {
				return os.Chmod(path, 0700)
			}
			return nil
		})
	})
	var members []protocolv2.Repository
	for _, name := range []string{"api", "web"} {
		repo := filepath.Join(root, name)
		if err := os.Mkdir(repo, 0700); err != nil {
			t.Fatal(err)
		}
		cliGit(t, repo, "init", "-b", "main")
		for file, content := range map[string]string{"source.txt": name, ".gitignore": "/ignored/\n"} {
			if err := os.WriteFile(filepath.Join(repo, file), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
		}
		cliGit(t, repo, "add", ".")
		cliGit(t, repo, "commit", "-m", "base")
		members = append(members, protocolv2.Repository{Name: name, Repository: repo})
	}
	raw, _ := json.Marshal(members)
	file := filepath.Join(root, "members.json")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return store, file
}

func cliGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := gitcontrol.Command(context.Background(), dir, args...)
	cmd.Env = append(gitcontrol.Environment(os.Environ()), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err, string(out))
	}
	return strings.TrimSpace(string(out))
}

func localCLI(t *testing.T, store bench.Store, op string, args ...string) ([]byte, error) {
	t.Helper()
	command := []string{op, "--state", store.State, "--data", store.Data}
	command = append(command, args...)
	var out, stderr bytes.Buffer
	err := workspaceCLIContext(context.Background(), command, &out, &stderr)
	return out.Bytes(), err
}

func localCreate(t *testing.T, store bench.Store, members string) protocolv2.Mutation {
	t.Helper()
	raw, err := localCLI(t, store, "create", "--repositories", members, "--checkpoint-repository", "api", "--request-id", "create-local", "card")
	if err != nil {
		t.Fatal(err, string(raw))
	}
	var envelope struct{ Result protocolv2.Mutation }
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Result
}

func TestWorkspaceLocalReplayUnderNewHolderRefusesWithoutDisclosureOrEffects(t *testing.T) {
	store, members := workspaceFixture(t)
	created := localCreate(t, store, members)
	raw, err := localCLI(t, store, "lease", "--holder", "worker", "--request-id", "lease-local", "card")
	if err != nil {
		t.Fatal(err, string(raw))
	}
	var leased struct{ Result protocolv2.Mutation }
	_ = json.Unmarshal(raw, &leased)
	token := leased.Result.Grant.LeaseToken
	path := filepath.Join(created.RootPath, "api", "ignored")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(path, "private")
	if err := os.WriteFile(file, []byte("preserved private bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := store.Snapshot("card")
	raw, err = localCLI(t, store, "create", "--repositories", members, "--checkpoint-repository", "api", "--request-id", "create-local", "card")
	var response protocolv2.Response
	request := protocolv2.Request{Version: protocolv2.Version, Operation: "create", Key: "card", RequestID: "create-local"}
	response, parseErr := protocolv2.ParseResponse(raw, 409, request)
	if err == nil || parseErr != nil || response.Result != nil || response.Error.Code != "conflict" || strings.Contains(string(raw), token) || strings.Contains(string(raw), created.RootPath) {
		t.Fatal("local creation replay exposed later owner/path", err, parseErr, string(raw))
	}
	after, _ := store.Snapshot("card")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("local replay changed owner")
	}
	if b, err := os.ReadFile(file); err != nil || string(b) != "preserved private bytes" {
		t.Fatal("local replay changed files", err)
	}
}

func TestWorkspaceLocalArchiveReviewedReapAndCompletedCreationRefusal(t *testing.T) {
	store, members := workspaceFixture(t)
	created := localCreate(t, store, members)
	if raw, err := localCLI(t, store, "archive", "--archive-grace", "0", "card"); err != nil {
		t.Fatal(err, string(raw))
	}
	assertReplay := func() {
		t.Helper()
		before, _ := store.Snapshot("card")
		raw, err := localCLI(t, store, "create", "--repositories", members, "--checkpoint-repository", "api", "--request-id", "create-local", "card")
		if err == nil || strings.Contains(string(raw), created.RootPath) {
			t.Fatal("archived/reaped creation replay recreated authority", err)
		}
		after, _ := store.Snapshot("card")
		if !reflect.DeepEqual(before, after) {
			t.Fatal("completed creation replay changed retained state")
		}
	}
	assertReplay()
	raw, err := localCLI(t, store, "plan-reap", "card")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := workspacearchive.ReadPlan(raw)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(filepath.Dir(store.State), "reviewed.json")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := store.Snapshot("card")
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range [][]string{{"--repository-name", "api"}, {"--holder", "worker"}, {"--token", "synthetic"}, {"--request-id", "ignored-id"}, {"--initialize=false"}, {"--socket", ""}} {
		args := append([]string{"--plan", file, "--approve", plan.Digest}, flag...)
		args = append(args, "card")
		if out, err := localCLI(t, store, "reap", args...); err == nil || len(out) != 0 {
			t.Fatal("inapplicable reap flag reached deletion", flag, err)
		}
		if _, err := os.Stat(created.RootPath); err != nil {
			t.Fatal("flag refusal deleted source", flag, err)
		}
		after, err := store.Snapshot("card")
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("flag refusal changed durable archive", flag, err)
		}
	}
	if _, err := localCLI(t, store, "reap", "--plan", file, "--approve", "wrong", "card"); err == nil {
		t.Fatal("reap accepted wrong approval")
	}
	if _, err := os.Stat(created.RootPath); err != nil {
		t.Fatal("refusal deleted source", err)
	}
	if raw, err := localCLI(t, store, "reap", "--plan", file, "--approve", plan.Digest, "card"); err != nil || !strings.Contains(string(raw), `"phase":"reaped"`) {
		t.Fatal("reviewed aggregate reap failed", err, string(raw))
	}
	assertReplay()
	if _, err := os.Lstat(created.RootPath); !os.IsNotExist(err) {
		t.Fatal("replayed creation materialized reaped root", err)
	}
}
