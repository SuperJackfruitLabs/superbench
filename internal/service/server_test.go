package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/client"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocol"
)

func serviceFixture(t *testing.T, operator, reader bool) (Config, string) {
	t.Helper()
	root, e := os.MkdirTemp("/tmp", "sb-api-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	uid := uint32(os.Geteuid())
	c := Config{Socket: filepath.Join(root, "api.sock"), Store: bench.Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data")}, Operators: map[uint32]bool{uid: operator}, Readers: map[uint32]bool{uid: reader}}
	return c, root
}
func startService(t *testing.T, c Config) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var serveErr error
	go func() { serveErr = Serve(ctx, c); close(done) }()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", c.Socket)
	}}
	httpClient := &http.Client{Transport: transport, Timeout: time.Second}
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		cancel()
		select {
		case <-done:
			if serveErr != nil {
				t.Error(serveErr)
			}
		case <-time.After(15 * time.Second):
			t.Error("daemon failed to stop")
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			t.Fatalf("daemon stopped: %v", serveErr)
		default:
		}
		r, e := httpClient.Get("http://local/healthz")
		if e == nil {
			r.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("daemon did not start")
}
func gitFixture(t *testing.T, root string) string {
	t.Helper()
	git := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, e := c.CombinedOutput(); e != nil {
			t.Fatalf("git: %v %s", e, out)
		}
	}
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	git(root, "init", "--bare", remote)
	git(root, "init", "-b", "main", seed)
	if e := os.WriteFile(filepath.Join(seed, "file"), []byte("base"), 0600); e != nil {
		t.Fatal(e)
	}
	git(seed, "add", ".")
	git(seed, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "base")
	git(seed, "remote", "add", "origin", remote)
	git(seed, "push", "origin", "main")
	git(remote, "symbolic-ref", "HEAD", "refs/heads/main")
	return remote
}
func call(t *testing.T, c Config, r protocol.Request) json.RawMessage {
	t.Helper()
	r.Version = protocol.Version
	body, e := client.Call(context.Background(), c.Socket, r)
	if e != nil {
		t.Fatal(e)
	}
	return body
}
func requireCode(t *testing.T, c Config, r protocol.Request, code string) {
	t.Helper()
	r.Version = protocol.Version
	_, e := client.Call(context.Background(), c.Socket, r)
	var api *protocol.APIError
	if !errors.As(e, &api) || api.Code != code {
		t.Fatalf("wanted %s, got %v", code, e)
	}
}
func TestUnixLifecycleAndReceiptReplay(t *testing.T) {
	c, root := serviceFixture(t, true, false)
	remote := gitFixture(t, root)
	startService(t, c)
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	create := protocol.Request{Operation: "create", Key: "task", Repository: remote, RequestID: "create-1"}
	first := call(t, c, create)
	if !bytes.Equal(first, call(t, c, create)) {
		t.Fatal("create replay changed")
	}
	changed := create
	changed.Key = "other"
	requireCode(t, c, changed, "conflict")
	lease := protocol.Request{Operation: "lease", Key: "task", Holder: "first", RequestID: "lease-1"}
	leased := call(t, c, lease)
	var result struct{ Bench benchDTO }
	if e := json.Unmarshal(leased, &result); e != nil {
		t.Fatal(e)
	}
	token := result.Bench.Lease.Token
	if token == "" {
		t.Fatal("lease omitted capability")
	}
	status := call(t, c, protocol.Request{Operation: "status", Key: "task"})
	if bytes.Contains(status, []byte(token)) || bytes.Contains(status, []byte(`"token"`)) {
		t.Fatal("status leaked capability")
	}
	requireCode(t, c, protocol.Request{Operation: "release", Key: "task", LeaseToken: strings.Repeat("0", 32)}, "forbidden")
	call(t, c, protocol.Request{Operation: "checkpoint", Key: "task", LeaseToken: token})
	call(t, c, protocol.Request{Operation: "release", Key: "task", LeaseToken: token})
	second := call(t, c, protocol.Request{Operation: "lease", Key: "task", Holder: "second", RequestID: "lease-2"})
	if bytes.Equal(leased, second) {
		t.Fatal("new lease reused old capability")
	}
	if !bytes.Equal(leased, call(t, c, lease)) {
		t.Fatal("lease receipt did not replay exactly")
	}
	requireCode(t, c, protocol.Request{Operation: "release", Key: "task", LeaseToken: token}, "forbidden")
	states, e := c.Store.Status("task")
	if e != nil || states[0].Lease.Holder != "second" {
		t.Fatal("receipt replay mutated new holder", e)
	}
	requireCode(t, c, protocol.Request{Operation: "status", Key: "absent"}, "not_found")
}
func rawCall(t *testing.T, c Config, method, body, media string) (int, []byte) {
	t.Helper()
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", c.Socket)
	}}
	defer tr.CloseIdleConnections()
	r, e := http.NewRequest(method, "http://local/v1/operations", strings.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	r.Header.Set("Content-Type", media)
	r.Header.Set("X-Operator-UID", "0")
	r.Header.Set("Authorization", "Bearer forged-authority")
	res, e := (&http.Client{Transport: tr, Timeout: time.Second}).Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	out, e := io.ReadAll(res.Body)
	if e != nil {
		t.Fatal(e)
	}
	return res.StatusCode, out
}
func TestPeerAuthorization(t *testing.T) {
	for _, reader := range []bool{false, true} {
		t.Run(map[bool]string{false: "denied", true: "reader"}[reader], func(t *testing.T) {
			c, _ := serviceFixture(t, false, reader)
			startService(t, c)
			want := 403
			if reader {
				want = 200
			}
			status, _ := rawCall(t, c, "POST", `{"version":"superbench.v1","operation":"status"}`, "application/json")
			if status != want {
				t.Fatalf("status %d", status)
			}
			requireCode(t, c, protocol.Request{Operation: "stop_admin", Key: "task"}, "forbidden")
			requireCode(t, c, protocol.Request{Operation: "lease", Key: "task", Holder: "reader", RequestID: "forged"}, "forbidden")
		})
	}
}
func TestMalformedTransport(t *testing.T) {
	c, _ := serviceFixture(t, true, false)
	startService(t, c)
	for _, tc := range []struct {
		method, body, media string
		status              int
	}{
		{"GET", "", "application/json", 405},
		{"POST", `{}`, "text/plain", 415},
		{"POST", `{"version":"superbench.v1","operation":"status","operation":"stop_admin"}`, "application/json", 400},
		{"POST", `{"version":"superbench.v1","operation":"status","lease_token":"submitted-secret"}`, "application/json", 400},
		{"POST", `{"version":"superbench.v1","operation":"status"} {}`, "application/json", 400},
		{"POST", strings.Repeat("x", protocol.MaxBody+1), "application/json", 413},
	} {
		status, body := rawCall(t, c, tc.method, tc.body, tc.media)
		if status != tc.status {
			t.Fatalf("wanted %d got %d: %s", tc.status, status, body)
		}
		if bytes.Contains(body, []byte("submitted-secret")) {
			t.Fatal("invalid request echoed secret")
		}
	}
}
func TestSocketSingletonAndFilePreservation(t *testing.T) {
	c, root := serviceFixture(t, true, false)
	startService(t, c)
	if e := Serve(context.Background(), c); e == nil {
		t.Fatal("second daemon acquired same state")
	}
	call(t, c, protocol.Request{Operation: "status"})
	other := filepath.Join(root, "other-state")
	file := filepath.Join(root, "keep")
	if e := os.WriteFile(file, []byte("keep"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, lock, e := listen(file, other); e == nil {
		lock.Close()
		t.Fatal("replaced regular file")
	}
	data, e := os.ReadFile(file)
	if e != nil || string(data) != "keep" {
		t.Fatal("file changed", e)
	}
}
func TestRepositoryDisplayRedactsSecrets(t *testing.T) {
	got := displayRepository("https://user:password@example.invalid/repo?access_token=secret#private")
	if got != "https://user@example.invalid/repo" {
		t.Fatalf("repository not sanitized: %s", got)
	}
}
