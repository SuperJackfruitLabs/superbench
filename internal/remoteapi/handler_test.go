package remoteapi

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteca"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteproto"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotestate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

const (
	operator uint32 = 1000
	reader   uint32 = 2000
	address         = "100.64.0.1:2222"
)

// scopes is a cgroup-free Scopes: a created scope stays present until fenced.
type scopes struct {
	mu      sync.Mutex
	present map[string]bool
}

func (s *scopes) Create(_, id string) (execution.Identity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.present[id] = true
	return execution.Identity{BootID: "test-boot", HostID: "test-host"}, nil
}
func (s *scopes) Fence(e *execution.Execution) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.present, e.ID)
	return nil
}
func (s *scopes) Empty(*execution.Execution) error { return nil }
func (s *scopes) Present(e *execution.Execution) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.present[e.ID], nil
}
func (s *scopes) Start(*exec.Cmd, *execution.Execution) error { return errors.New("no process") }

type extension struct {
	fingerprint string
	claims      remoteca.Claims
	until       time.Time
}

type fixture struct {
	h       Handler
	m       remotestate.Manager
	token   string
	now     time.Time
	states  int
	extends []extension
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := gitcontrol.Command(context.Background(), dir, args...)
	cmd.Env = append(gitcontrol.Environment(os.Environ()), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

// newFixture is a ready v2 workspace "card" held by "worker", a real
// authority and a handler whose clock the test moves.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	store := bench.Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data")}
	repo := filepath.Join(root, "api")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "source.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "base")
	ctx := context.Background()
	spec := workspace.Spec{Key: "card", CheckpointRepository: "api", Repositories: []workspace.Repository{{Name: "api", Repository: repo}}}
	if _, err := store.InitializeWorkspaceContext(ctx, "creation", spec); err != nil {
		t.Fatal(err)
	}
	held, err := store.AcquireWorkspaceContext(ctx, "card", "worker")
	if err != nil {
		t.Fatal(err)
	}
	m, err := remotestate.ForStore(store, &scopes{present: map[string]bool{}})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := remoteca.Open(filepath.Join(root, "remote"))
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{m: m, token: held.Lease.Token, now: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)}
	m.Now = func() time.Time { return f.now }
	f.m = m
	f.h = Handler{
		Authority: authority,
		State:     func() (remotestate.Manager, error) { f.states++; return f.m, nil },
		Address:   address,
		Operators: map[uint32]bool{operator: true},
		Readers:   map[uint32]bool{reader: true},
		Extend: func(fp string, c remoteca.Claims, until time.Time) {
			f.extends = append(f.extends, extension{fp, c, until})
		},
		Now: func() time.Time { return f.now },
	}
	return f
}

func (f *fixture) record(t *testing.T) *model.Bench {
	t.Helper()
	var b *model.Bench
	if err := f.m.Prep.Read(func(rows benchstate.Database) error { b = workspace.Clone(rows["card"]); return nil }); err != nil || b == nil {
		t.Fatal("read", err)
	}
	return b
}

func (f *fixture) call(t *testing.T, uid uint32, path string, body any) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", path, bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.h.Serve(w, r, uid, nil)
	if w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing reply headers")
	}
	return w.Code, w.Body.Bytes()
}

func (f *fixture) credential(t *testing.T, uid uint32, public string) (int, []byte) {
	t.Helper()
	return f.call(t, uid, "/v2/remote/credentials", remoteproto.CredentialRequest{Version: remoteproto.Version, Operation: "writer_certificate", Key: "card", LeaseToken: f.token, PublicKey: public})
}

func ed25519Line(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))) + " agent@hub"
}

func refusal(t *testing.T, body []byte, operation, code string) {
	t.Helper()
	var r remoteproto.Response
	if err := json.Unmarshal(body, &r); err != nil || r.OK || r.Result != nil || r.Error == nil || r.Error.Code != code || r.Version != remoteproto.Version || r.Operation != operation {
		t.Fatalf("want %s refusal, got %s", code, body)
	}
}

func TestCredentialRequiresOperator(t *testing.T) {
	f := newFixture(t)
	for _, uid := range []uint32{reader, 4242} {
		status, body := f.credential(t, uid, ed25519Line(t))
		if status != 403 {
			t.Fatal("non-operator issued", uid, status, string(body))
		}
		refusal(t, body, "writer_certificate", "forbidden")
		if bytes.Contains(body, []byte(f.token)) {
			t.Fatal("refusal echoes the token")
		}
	}
	if f.states != 0 {
		t.Fatal("State called for a non-operator")
	}
}

func TestCredentialIssuesCertificate(t *testing.T) {
	f := newFixture(t)
	public := ed25519Line(t)
	status, body := f.credential(t, operator, public)
	if status != 200 {
		t.Fatal(status, string(body))
	}
	if bytes.Contains(body, []byte(f.token)) {
		t.Fatal("reply contains the lease token")
	}
	result, err := remoteproto.ParseResponse(body, status, "writer_certificate")
	if err != nil {
		t.Fatal(err)
	}
	var c remoteproto.Credential
	if err := remoteproto.Decode(result, &c); err != nil {
		t.Fatal(err)
	}
	b := f.record(t)
	if c.User != "b-"+b.Workspace.ID || c.Address != address || c.LeaseID != b.Lease.ID || c.Generation != b.Lease.Generation || c.Generation < 1 {
		t.Fatal("credential does not name the lease", c)
	}
	if !strings.HasPrefix(c.HostKeyFingerprint, "SHA256:") || c.HostKeyFingerprint != f.h.Authority.HostFingerprint() || c.HostKey != f.h.Authority.HostKeyLine() {
		t.Fatal("host key", c)
	}
	if c.ValidBefore != f.now.Add(remoteca.WriterValidity).Format(time.RFC3339) {
		t.Fatal("valid_before", c.ValidBefore)
	}
	parsed, _, _, _, err := ssh.ParseAuthorizedKey([]byte(c.Certificate))
	cert, ok := parsed.(*ssh.Certificate)
	if err != nil || !ok || cert.KeyId != c.LeaseID || len(cert.ValidPrincipals) != 1 || cert.ValidPrincipals[0] != c.User {
		t.Fatal("certificate", err, cert)
	}
	if claims, _, err := f.h.Authority.Verify(c.User, cert, f.now); err != nil || claims.ExecutionID != b.Lease.Execution.ID {
		t.Fatal("certificate does not verify for the remote execution", claims, err)
	}
	st, err := f.m.Status("card", 50)
	if err != nil || len(st.Certificates) != 1 || st.Certificates[0].Serial != cert.Serial || st.Certificates[0].Holder != "worker" {
		t.Fatal("issued certificate not audited", st, err)
	}
	if len(f.extends) != 1 || f.extends[0].claims.LeaseID != c.LeaseID || f.extends[0].fingerprint != st.Certificates[0].Fingerprint {
		t.Fatal("extend", f.extends)
	}
}

func TestCredentialRefusesBadKey(t *testing.T) {
	f := newFixture(t)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaPub, _ := ssh.NewPublicKey(&rsaKey.PublicKey)
	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	signer, _ := ssh.NewSignerFromKey(ecKey)
	_, edPriv, _ := ed25519.GenerateKey(rand.Reader)
	edPub, _ := ssh.NewPublicKey(edPriv.Public())
	cert := &ssh.Certificate{Key: edPub, CertType: ssh.UserCert, ValidBefore: ssh.CertTimeInfinity}
	if err := cert.SignCert(rand.Reader, signer); err != nil {
		t.Fatal(err)
	}
	for name, line := range map[string]string{
		"rsa":         string(bytes.TrimSpace(ssh.MarshalAuthorizedKey(rsaPub))),
		"cert-as-key": string(bytes.TrimSpace(ssh.MarshalAuthorizedKey(cert))),
		"garbage":     "ssh-ed25519 not-base64",
	} {
		status, body := f.credential(t, operator, line)
		if status != 400 {
			t.Fatal(name, status, string(body))
		}
		refusal(t, body, "writer_certificate", "invalid_request")
	}
	st, err := f.m.Status("", 50)
	if err != nil || len(st.Certificates) != 0 || len(f.extends) != 0 || f.record(t).Lease.Execution != nil {
		t.Fatal("refused key granted, audited or extended", st, err)
	}
	// benchd's own host key parses, so it is granted, but IssueWriter refuses it.
	status, body := f.credential(t, operator, f.h.Authority.HostKeyLine())
	if status != 400 {
		t.Fatal("host key", status, string(body))
	}
	refusal(t, body, "writer_certificate", "invalid_request")
	if st, err = f.m.Status("", 50); err != nil || len(st.Certificates) != 0 || len(f.extends) != 0 {
		t.Fatal("refused host key audited or extended", st, err)
	}
}

func TestCredentialRefusalCodes(t *testing.T) {
	f := newFixture(t)
	public := ed25519Line(t)
	cases := map[string]struct {
		request remoteproto.CredentialRequest
		setup   func()
		status  int
		code    string
	}{
		"wrong-token":   {remoteproto.CredentialRequest{Key: "card", LeaseToken: strings.Repeat("0", 32)}, nil, 403, "forbidden"},
		"unknown-bench": {remoteproto.CredentialRequest{Key: "other", LeaseToken: f.token}, nil, 404, "not_found"},
		"stopped": {remoteproto.CredentialRequest{Key: "card", LeaseToken: f.token}, func() {
			if err := f.m.Prep.Update(func(rows benchstate.Database, _ *sql.Tx) error {
				rows["card"].Lease.StopRequested, rows["card"].Lease.Continuation = true, ""
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}, 409, "conflict"},
	}
	for _, name := range []string{"wrong-token", "unknown-bench", "stopped"} {
		c := cases[name]
		if c.setup != nil {
			c.setup()
		}
		c.request.Version, c.request.Operation, c.request.PublicKey = remoteproto.Version, "writer_certificate", public
		status, body := f.call(t, operator, "/v2/remote/credentials", c.request)
		if status != c.status {
			t.Fatal(name, status, string(body))
		}
		refusal(t, body, "writer_certificate", c.code)
		if bytes.Contains(body, []byte(f.token)) {
			t.Fatal(name, "refusal echoes the token")
		}
	}
	f.h.State = func() (remotestate.Manager, error) { return remotestate.Manager{}, nil }
	status, body := f.credential(t, operator, public)
	if status != 503 {
		t.Fatal("unavailable state not 503", status, string(body))
	}
	refusal(t, body, "writer_certificate", "unavailable")
}

func TestCredentialRecordFailureWithholdsCertificate(t *testing.T) {
	f := newFixture(t)
	update, calls := f.m.Prep.Update, 0
	// Grant uses two updates; the third is RecordCertificate's.
	f.m.Prep.Update = func(fn func(benchstate.Database, *sql.Tx) error) error {
		if calls++; calls == 3 {
			return errors.New("disk full")
		}
		return update(fn)
	}
	status, body := f.credential(t, operator, ed25519Line(t))
	if status != 503 || bytes.Contains(body, []byte(`"certificate":`)) || bytes.Contains(body, []byte("cert-v01")) || len(f.extends) != 0 {
		t.Fatal("certificate returned without an audit row", status, string(body), f.extends)
	}
	refusal(t, body, "writer_certificate", "unavailable")
}

func TestCredentialRenewalExtends(t *testing.T) {
	f := newFixture(t)
	public := ed25519Line(t)
	var certs []string
	for range 2 {
		status, body := f.credential(t, operator, public)
		if status != 200 {
			t.Fatal(status, string(body))
		}
		result, _ := remoteproto.ParseResponse(body, status, "writer_certificate")
		var c remoteproto.Credential
		if err := remoteproto.Decode(result, &c); err != nil {
			t.Fatal(err)
		}
		certs = append(certs, c.Certificate)
		f.now = f.now.Add(10 * time.Minute)
	}
	if len(f.extends) != 2 || !f.extends[1].until.After(f.extends[0].until) || f.extends[0].claims != f.extends[1].claims || f.extends[0].fingerprint != f.extends[1].fingerprint {
		t.Fatal("renewal did not extend the same connections", f.extends)
	}
	if certs[0] == certs[1] {
		t.Fatal("renewal returned the same certificate")
	}
}

func TestRemoteStatusOpenToReaders(t *testing.T) {
	f := newFixture(t)
	if status, body := f.credential(t, operator, ed25519Line(t)); status != 200 {
		t.Fatal(status, string(body))
	}
	for _, uid := range []uint32{reader, operator} {
		status, body := f.call(t, uid, "/v2/remote/status", remoteproto.StatusRequest{Version: remoteproto.Version, Operation: "remote_status"})
		if status != 200 || bytes.Contains(body, []byte(f.token)) {
			t.Fatal(status, string(body))
		}
		result, err := remoteproto.ParseResponse(body, status, "remote_status")
		var st remoteproto.Status
		if err != nil || remoteproto.Decode(result, &st) != nil {
			t.Fatal("status reply", string(body), err)
		}
		b := f.record(t)
		if st.Address != address || st.HostKeyFingerprint != f.h.Authority.HostFingerprint() || len(st.Remote) != 1 || st.Remote[0].LeaseID != b.Lease.ID || len(st.Certificates) != 1 || st.Certificates[0].Holder != "worker" || st.Sessions == nil {
			t.Fatal("status", st)
		}
	}
	status, body := f.call(t, 4242, "/v2/remote/status", remoteproto.StatusRequest{Version: remoteproto.Version, Operation: "remote_status"})
	if status != 403 {
		t.Fatal("unknown uid read status", status, string(body))
	}
	refusal(t, body, "remote_status", "forbidden")
}

func TestRemoteRequestShape(t *testing.T) {
	f := newFixture(t)
	serve := func(method, path, media string, body []byte, admit func(*http.Request) error) (int, []byte) {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Content-Type", media)
		w := httptest.NewRecorder()
		f.h.Serve(w, r, operator, admit)
		return w.Code, w.Body.Bytes()
	}
	valid, _ := json.Marshal(remoteproto.StatusRequest{Version: remoteproto.Version, Operation: "remote_status"})
	for name, c := range map[string]struct {
		method, path, media string
		body                []byte
		admit               func(*http.Request) error
		status              int
	}{
		"get":       {"GET", "/v2/remote/status", "application/json", valid, nil, 405},
		"media":     {"POST", "/v2/remote/status", "text/plain", valid, nil, 415},
		"unknown":   {"POST", "/v2/remote/other", "application/json", valid, nil, 404},
		"too-large": {"POST", "/v2/remote/status", "application/json", bytes.Repeat([]byte(" "), remoteproto.MaxBody+1), nil, 413},
		"shape":     {"POST", "/v2/remote/status", "application/json", []byte(`{}`), nil, 400},
		"admission": {"POST", "/v2/remote/status", "application/json", valid, func(*http.Request) error { return errors.New("full") }, 503},
	} {
		if status, body := serve(c.method, c.path, c.media, c.body, c.admit); status != c.status {
			t.Error(name, status, string(body))
		}
	}
}
