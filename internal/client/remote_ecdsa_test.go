package client

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteca"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteproto"
)

// An ecdsa client key gets a certificate that starts ecdsa-sha2-nistp256-cert,
// and the client must accept that reply.
func TestWriterCertificateAcceptsECDSAReply(t *testing.T) {
	dir, err := os.MkdirTemp("", "rcl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	authority, err := remoteca.Open(filepath.Join(dir, "remote"))
	if err != nil {
		t.Fatal(err)
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public, _ := ssh.NewPublicKey(&k.PublicKey)
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(public)))
	claims := remoteca.Claims{BenchID: strings.Repeat("1", 32), LeaseID: strings.Repeat("2", 32), Generation: 3, ExecutionID: strings.Repeat("3", 32)}
	issued, err := authority.IssueWriter(line, claims, time.Now())
	if err != nil || !strings.HasPrefix(issued.Line, "ecdsa-sha2-nistp256-cert-v01@openssh.com ") {
		t.Fatalf("issued %q %v", issued.Line, err)
	}
	var reply remoteproto.Credential
	reply.Certificate, reply.User, reply.Address = issued.Line, remoteca.Principal(claims.BenchID), "100.64.0.1:2222"
	reply.HostKey, reply.HostKeyFingerprint = authority.HostKeyLine(), authority.HostFingerprint()
	reply.LeaseID, reply.Generation, reply.ValidBefore = claims.LeaseID, claims.Generation, issued.ValidBefore.UTC().Format(time.RFC3339)
	reply.WorkingDirectory = "/data/benches/card"
	socket := filepath.Join(dir, "s")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, _ := json.Marshal(reply)
		raw, _ := json.Marshal(remoteproto.Response{Version: remoteproto.Version, Operation: remoteproto.OperationCredential, OK: true, Result: result})
		w.Header().Set("Content-Type", "application/json")
		w.Write(raw)
	})}
	go server.Serve(l)
	t.Cleanup(func() { server.Close() })
	got, err := WriterCertificate(context.Background(), socket, remoteproto.CredentialRequest{Key: "card", LeaseToken: strings.Repeat("a", 32), PublicKey: line})
	if err != nil || got != reply {
		t.Fatalf("client refused an ecdsa reply: %v", err)
	}
}
