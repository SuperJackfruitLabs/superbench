package remoteaccess

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/activation"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteproto"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotessh/remotesshtest"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotestate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceops"
)

const operator, reader, stranger = 42, 43, 44

// scopes stands in for the host's cgroups. A scope is present from Create
// until Fence; processes a test places in a scope are killed by its fence,
// as cgroup.kill kills everything in the real one.
type scopes struct {
	mu      sync.Mutex
	present map[string]bool
	pids    map[string][]int
	fenced  []string
	fenceAt time.Time
}

func newScopes() *scopes { return &scopes{present: map[string]bool{}, pids: map[string][]int{}} }

func (s *scopes) Create(_, id string) (execution.Identity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.present[id] = true
	return execution.Identity{BootID: "test-boot", HostID: "test-host"}, nil
}
func (s *scopes) Fence(e *execution.Execution) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fenced, s.fenceAt = append(s.fenced, e.ID), time.Now()
	for _, pid := range s.pids[e.ID] {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	delete(s.pids, e.ID)
	delete(s.present, e.ID)
	return nil
}
func (s *scopes) Empty(e *execution.Execution) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.present[e.ID] {
		return errors.New("scope still populated")
	}
	return nil
}
func (s *scopes) Present(e *execution.Execution) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.present[e.ID], nil
}

// place puts pid in the scope of execution id.
func (s *scopes) place(id string, pid int) {
	s.mu.Lock()
	s.pids[id] = append(s.pids[id], pid)
	s.mu.Unlock()
}
func (s *scopes) gone(id string) { s.mu.Lock(); delete(s.present, id); s.mu.Unlock() }
func (s *scopes) fences() ([]string, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.fenced...), s.fenceAt
}

type fixture struct {
	ops    workspaceops.Manager
	scopes *scopes
	access *Access
	tokens map[string]string // key -> lease token
	leases []string          // every token granted, oldest first
	addr   string
	done   chan error
	cancel context.CancelFunc
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := gitcontrol.Command(context.Background(), dir, args...)
	cmd.Env = append(gitcontrol.Environment(os.Environ()), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

// newFixture is one ready v2 workspace per key, each leased to "worker", as
// workspaceops' own fixture builds them, and an Access built by New over the
// same store with the test launcher, fake scopes and a pre-bound listener.
// The Access is not serving yet.
func newFixture(t *testing.T, keys ...string) *fixture {
	t.Helper()
	root, err := os.MkdirTemp("", "sb-remote-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	if root, err = filepath.EvalSymlinks(root); err != nil {
		t.Fatal(err)
	}
	f := &fixture{scopes: newScopes(), tokens: map[string]string{}}
	f.ops = workspaceops.Manager{Store: bench.Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data")}, RemoteScopes: f.scopes}
	var repos []protocolv2.Repository
	for _, name := range []string{"api", "web"} {
		repo := filepath.Join(root, name)
		if err := os.Mkdir(repo, 0700); err != nil {
			t.Fatal(err)
		}
		git(t, repo, "init", "-b", "main")
		if err := os.WriteFile(filepath.Join(repo, "source.txt"), []byte(name+" base\n"), 0600); err != nil {
			t.Fatal(err)
		}
		git(t, repo, "add", ".")
		git(t, repo, "commit", "-m", "base")
		repos = append(repos, protocolv2.Repository{Name: name, Repository: repo})
	}
	yes := true
	for _, key := range keys {
		create := protocolv2.Request{Version: protocolv2.Version, Operation: "create", Key: key, RequestID: "create-" + key, Initialize: &yes, CheckpointRepository: "api", Repositories: repos}
		if _, err := f.ops.Execute(context.Background(), create, operator); err != nil {
			t.Fatal(err)
		}
		f.lease(t, key)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	f.addr = l.Addr().String()
	on := activation.Switches{FreshPayload: true, RemoteAccess: true, RemoteFleetAddress: netip.MustParseAddrPort(f.addr)}
	if f.access, err = New(f.ops.Store, on, map[uint32]bool{operator: true}, map[uint32]bool{reader: true}); err != nil {
		t.Fatal(err)
	}
	f.access.launcher, f.access.scopes, f.access.listener, f.access.host = remotesshtest.Local{}, f.scopes, l, meetsRequirements()
	return f
}

// lease acquires key for "worker" and keeps its token.
func (f *fixture) lease(t *testing.T, key string) string {
	t.Helper()
	held, err := f.ops.Execute(context.Background(), protocolv2.Request{Version: protocolv2.Version, Operation: "lease", Key: key, Holder: "worker", RequestID: fmt.Sprintf("lease-%s-%d", key, len(f.leases))}, operator)
	if err != nil || held.GrantToken == "" {
		t.Fatal("lease", err)
	}
	f.tokens[key] = held.GrantToken
	f.leases = append(f.leases, held.GrantToken)
	return held.GrantToken
}

// op runs a token-owned workspace operation on key.
func (f *fixture) op(t *testing.T, op, key string) {
	t.Helper()
	if _, err := f.ops.Execute(context.Background(), protocolv2.Request{Version: protocolv2.Version, Operation: op, Key: key, LeaseToken: f.tokens[key]}, operator); err != nil {
		t.Fatal(op, err)
	}
}

// serve runs Access.Serve until the test ends, and requires a clean return.
func (f *fixture) serve(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	f.done, f.cancel = make(chan error, 1), cancel
	go func() { f.done <- f.access.Serve(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.access.mu.Lock()
		ready := f.access.authority != nil
		f.access.mu.Unlock()
		if ready {
			break
		}
		select {
		case err := <-f.done:
			t.Fatal("serve returned", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("serve did not open the authority")
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-f.done:
			if err != nil {
				t.Error("serve", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("serve did not return")
		}
	})
}

// route sends one request through Access.Route as uid.
func (f *fixture) route(t *testing.T, uid uint32, path string, body any) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "http://local"+path, bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.access.Route(w, r, uid, nil)
	return w.Code, w.Body.Bytes()
}

// client is a client key, as a stock ssh client keeps it.
type client struct {
	signer     ssh.Signer
	authorized string // public key, authorized_keys form
	pem        []byte // OpenSSH private key
}

func newClient(t *testing.T) client {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	return client{signer: signer, authorized: string(bytes.TrimSpace(ssh.MarshalAuthorizedKey(signer.PublicKey()))), pem: pem.EncodeToMemory(block)}
}

func credentialRequest(key, token, publicKey string) remoteproto.CredentialRequest {
	return remoteproto.CredentialRequest{Version: remoteproto.Version, Operation: remoteproto.OperationCredential, Key: key, LeaseToken: token, PublicKey: publicKey}
}

// credential asks Route for a writer certificate for c on key's lease.
func (f *fixture) credential(t *testing.T, key string, c client) (remoteproto.Credential, []byte) {
	t.Helper()
	status, body := f.route(t, operator, "/v2/remote/credentials", credentialRequest(key, f.tokens[key], c.authorized))
	if status != http.StatusOK {
		t.Fatalf("credential %d %s", status, body)
	}
	var reply struct {
		OK     bool                   `json:"ok"`
		Result remoteproto.Credential `json:"result"`
	}
	if err := json.Unmarshal(body, &reply); err != nil || !reply.OK {
		t.Fatalf("credential reply %v %s", err, body)
	}
	return reply.Result, body
}

// status reads remote status for key as the reader.
func (f *fixture) status(t *testing.T, key string) (remoteproto.Status, []byte) {
	t.Helper()
	code, body := f.route(t, reader, "/v2/remote/status", remoteproto.StatusRequest{Version: remoteproto.Version, Operation: remoteproto.OperationStatus, Key: key})
	if code != http.StatusOK {
		t.Fatalf("status %d %s", code, body)
	}
	var reply struct {
		Result remoteproto.Status `json:"result"`
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		t.Fatal(err)
	}
	return reply.Result, body
}

// sessions waits until key's audit holds n ended sessions, newest first.
func (f *fixture) sessions(t *testing.T, key string, n int, within time.Duration) []remoteproto.Session {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		s, _ := f.status(t, key)
		ended := 0
		for _, x := range s.Sessions {
			if x.EndReason != "" {
				ended++
			}
		}
		if ended >= n {
			return s.Sessions
		}
		if time.Now().After(deadline) {
			t.Fatalf("sessions %+v", s.Sessions)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// execution is the current remote execution ID for key, read through remotestate.
func (f *fixture) execution(t *testing.T, key string) string {
	t.Helper()
	m, err := remotestate.ForStore(f.ops.Store, f.scopes)
	if err != nil {
		t.Fatal(err)
	}
	s, err := m.Status(key, 1)
	if err != nil || len(s.Remote) != 1 {
		t.Fatal("remote execution", err, s.Remote)
	}
	return s.Remote[0].ExecutionID
}

// meetsRequirements is a host that has every deploy requirement, so tests
// may keep their roots in the system temporary directory.
func meetsRequirements() *host {
	return &host{varTmp: "/", devShm: "/", mountSetattr: func() error { return nil }}
}
