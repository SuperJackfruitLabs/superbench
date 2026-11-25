package remotecli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteproto"
)

const token = "0123456789abcdef0123456789abcdef"

var credential = remoteproto.Credential{
	Certificate: "ssh-ed25519-cert-v01@openssh.com AAAAcert", User: "b-" + strings.Repeat("b", 32), Address: "100.64.0.1:2222",
	HostKey: "ssh-ed25519 AAAAhost", HostKeyFingerprint: "SHA256:host", LeaseID: strings.Repeat("5", 32), Generation: 2, ValidBefore: "2026-11-24T12:30:00Z",
}

// serve runs a fake benchd socket that records each request body.
func serve(t *testing.T, handle func(path string, body []byte) (int, any)) (string, *[][]byte) {
	t.Helper()
	dir, err := os.MkdirTemp("", "rcli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var seen [][]byte
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, body)
		status, result := handle(r.URL.Path, body)
		var operation map[string]string
		_ = json.Unmarshal(body, &operation)
		response := remoteproto.Response{Version: remoteproto.Version, Operation: operation["operation"], OK: status == 200}
		if status == 200 {
			response.Result, _ = json.Marshal(result)
		} else {
			response.Error = result.(*remoteproto.APIError)
		}
		raw, _ := json.Marshal(response)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write(raw)
	})}
	go server.Serve(l)
	t.Cleanup(func() { server.Close() })
	return socket, &seen
}

func TestRemoteCredentialCLI(t *testing.T) {
	socket, seen := serve(t, func(path string, body []byte) (int, any) {
		if path != "/v2/remote/credentials" {
			return 404, &remoteproto.APIError{Code: "not_found", Message: "unknown endpoint"}
		}
		return 200, credential
	})
	t.Setenv("BENCH_LEASE_TOKEN", token)
	dir := t.TempDir()
	public, out := filepath.Join(dir, "id.pub"), filepath.Join(dir, "id-cert.pub")
	if err := os.WriteFile(public, []byte("ssh-ed25519 AAAAkey agent@hub\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), "remote-credential", []string{"--socket", socket, "--public-key", public, "--certificate-out", out, "card"}, &stdout, &stderr); err != nil {
		t.Fatal(err, stderr.String())
	}
	request, err := remoteproto.ParseCredentialRequest((*seen)[0])
	if err != nil || request.LeaseToken != token || request.Key != "card" || request.PublicKey != "ssh-ed25519 AAAAkey agent@hub" {
		t.Fatal("request", request, err)
	}
	var printed remoteproto.Credential
	if err := json.Unmarshal(stdout.Bytes(), &printed); err != nil || printed != credential {
		t.Fatal("printed", stdout.String(), err)
	}
	if bytes.Contains(stdout.Bytes(), []byte(token)) || bytes.Contains(stderr.Bytes(), []byte(token)) {
		t.Fatal("output shows the lease token")
	}
	written, err := os.ReadFile(out)
	if err != nil || string(written) != credential.Certificate+"\n" {
		t.Fatal("certificate file", string(written), err)
	}
	if st, err := os.Stat(out); err != nil || st.Mode().Perm() != 0644 {
		t.Fatal("certificate file mode", st.Mode(), err)
	}
	if err := Run(context.Background(), "remote-credential", []string{"--socket", socket, "--public-key", public, "--token", strings.Repeat("a", 32), "card"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if request, _ = remoteproto.ParseCredentialRequest((*seen)[1]); request.LeaseToken != strings.Repeat("a", 32) {
		t.Fatal("--token not used")
	}
}

func TestRemoteCredentialCLIRefusals(t *testing.T) {
	socket, seen := serve(t, func(string, []byte) (int, any) {
		return 409, &remoteproto.APIError{Code: "conflict", Message: "lease is not usable for remote work"}
	})
	public := filepath.Join(t.TempDir(), "id.pub")
	if err := os.WriteFile(public, []byte("ssh-ed25519 AAAAkey\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BENCH_LEASE_TOKEN", token)
	for name, args := range map[string][]string{
		"no-socket": {"--public-key", public, "card"},
		"no-public": {"--socket", socket, "card"},
		"no-key":    {"--socket", socket, "--public-key", public},
		"two-keys":  {"--socket", socket, "--public-key", public, "card", "other"},
	} {
		if err := Run(context.Background(), "remote-credential", args, io.Discard, io.Discard); err == nil {
			t.Error(name, "accepted")
		}
	}
	if len(*seen) != 0 {
		t.Fatal("refused invocation reached the socket")
	}
	t.Setenv("BENCH_LEASE_TOKEN", "")
	if err := Run(context.Background(), "remote-credential", []string{"--socket", socket, "--public-key", public, "card"}, io.Discard, io.Discard); err == nil || len(*seen) != 0 {
		t.Fatal("missing token accepted", err)
	}
	t.Setenv("BENCH_LEASE_TOKEN", token)
	err := Run(context.Background(), "remote-credential", []string{"--socket", socket, "--public-key", public, "card"}, io.Discard, io.Discard)
	var api *remoteproto.APIError
	if !errors.As(err, &api) || api.Code != "conflict" || strings.Contains(err.Error(), token) {
		t.Fatal("refusal", err)
	}
}

func TestRemoteStatusCLI(t *testing.T) {
	status := remoteproto.Status{Address: "100.64.0.1:2222", HostKeyFingerprint: "SHA256:host", Remote: []remoteproto.RemoteExecution{{Key: "card", Generation: 1}}, Certificates: []remoteproto.Certificate{{Serial: 1 << 62, Key: "card"}}, Sessions: []remoteproto.Session{}}
	socket, seen := serve(t, func(path string, body []byte) (int, any) { return 200, status })
	var stdout bytes.Buffer
	if err := Run(context.Background(), "remote-status", []string{"--socket", socket, "card"}, &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	if r, err := remoteproto.ParseStatusRequest((*seen)[0]); err != nil || r.Key != "card" {
		t.Fatal("status request", r, err)
	}
	var printed remoteproto.Status
	if err := json.Unmarshal(stdout.Bytes(), &printed); err != nil || printed.Certificates[0].Serial != 1<<62 || printed.Address != status.Address {
		t.Fatal("printed", stdout.String(), err)
	}
	if err := Run(context.Background(), "remote-status", []string{"--socket", socket}, io.Discard, io.Discard); err != nil {
		t.Fatal("status for every key", err)
	}
	if r, _ := remoteproto.ParseStatusRequest((*seen)[1]); r.Key != "" {
		t.Fatal("keyless status sent a key")
	}
	if err := Run(context.Background(), "remote-other", nil, io.Discard, io.Discard); err == nil {
		t.Fatal("unknown operation accepted")
	}
}
