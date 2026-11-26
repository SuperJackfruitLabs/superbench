package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteproto"
)

const remoteResponseLimit = 1 << 20

var hex32 = regexp.MustCompile(`^[a-f0-9]{32}$`)

// WriterCertificate asks benchd for a writer certificate for r.PublicKey on the
// lease r.LeaseToken owns. A refusal is a *remoteproto.APIError.
func WriterCertificate(ctx context.Context, socket string, r remoteproto.CredentialRequest) (remoteproto.Credential, error) {
	var zero remoteproto.Credential
	r.Version, r.Operation = remoteproto.Version, remoteproto.OperationCredential
	raw, err := json.Marshal(r)
	if err != nil {
		return zero, err
	}
	if _, err := remoteproto.ParseCredentialRequest(raw); err != nil {
		return zero, err
	}
	var c remoteproto.Credential
	if err := remoteCall(ctx, socket, "/v2/remote/credentials", r.Operation, raw, &c); err != nil {
		return zero, err
	}
	if _, err := time.Parse(time.RFC3339, c.ValidBefore); err != nil || !remoteproto.IsCertificateLine(c.Certificate) || !strings.HasPrefix(c.User, "b-") ||
		!strings.HasPrefix(c.HostKeyFingerprint, "SHA256:") || c.HostKey == "" || c.Address == "" || !hex32.MatchString(c.LeaseID) || c.Generation < 1 {
		return zero, remoteproto.ErrResponse
	}
	return c, nil
}

// RemoteStatus lists remote executions and recent audit rows.
func RemoteStatus(ctx context.Context, socket string, r remoteproto.StatusRequest) (remoteproto.Status, error) {
	r.Version, r.Operation = remoteproto.Version, remoteproto.OperationStatus
	raw, err := json.Marshal(r)
	if err != nil {
		return remoteproto.Status{}, err
	}
	if _, err := remoteproto.ParseStatusRequest(raw); err != nil {
		return remoteproto.Status{}, err
	}
	var s remoteproto.Status
	if err := remoteCall(ctx, socket, "/v2/remote/status", r.Operation, raw, &s); err != nil {
		return remoteproto.Status{}, err
	}
	return s, nil
}

func remoteCall(ctx context.Context, socket, path, operation string, raw []byte, out any) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "unix", socket)
	}, MaxResponseHeaderBytes: 64 << 10, DisableCompression: true, MaxConnsPerHost: 1}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, "POST", "http://benchd.local"+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return fmt.Errorf("remote socket request failed: %w", err)
	}
	defer response.Body.Close()
	if media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type")); err != nil || media != "application/json" {
		return errors.New("remote service response is not JSON")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, remoteResponseLimit+1))
	if err != nil {
		return fmt.Errorf("remote response interrupted: %w", err)
	}
	if len(body) > remoteResponseLimit {
		return errors.New("remote service response exceeds limit")
	}
	result, err := remoteproto.ParseResponse(body, response.StatusCode, operation)
	if err != nil {
		return err
	}
	return remoteproto.Decode(result, out)
}
