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
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
)

// WorkspaceError carries a validated failure while the caller retains its
// authorized response result. Infrastructure failures never become exit zero.
type WorkspaceError struct {
	Code, Message string
	commandExit   int
}

func (e *WorkspaceError) Error() string { return e.Code + ": " + e.Message }
func (e *WorkspaceError) ExitCode() int {
	if e.commandExit > 0 && e.commandExit < 256 {
		return e.commandExit
	}
	return 1
}

// ErrorV2 applies only to an already strictly validated response.
func ErrorV2(response protocolv2.Response) error {
	if response.OK {
		return nil
	}
	if response.Error == nil {
		return protocolv2.ErrResponse
	}
	e := &WorkspaceError{Code: response.Error.Code, Message: response.Error.Message}
	if e.Code == "operation_failed" {
		var outcome *protocolv2.RunOutcome
		switch r := response.Result.(type) {
		case *protocolv2.RunOutcome:
			outcome = r
		case *protocolv2.SetupOutcome:
			outcome = &r.RunOutcome
		}
		if outcome != nil && outcome.ExitCode != nil {
			e.commandExit = *outcome.ExitCode
		}
	}
	return e
}

// CallV2 uses only the selected Unix socket and v2 endpoint. It never retries or
// falls back: an interrupted mutation must be reconciled with its request ID.
func CallV2(ctx context.Context, socket string, request protocolv2.Request) (protocolv2.Response, error) {
	var zero protocolv2.Response
	raw, err := json.Marshal(request)
	if err != nil {
		return zero, err
	}
	parse, contentType := protocolv2.Parse, "application/json"
	if request.IO != nil {
		// The explicit profile is the only way to send io; unprofiled bytes are unchanged.
		parse, contentType = protocolv2.ParseFinite, protocolv2.FiniteIOContentType
	}
	if _, err := parse(raw); err != nil {
		return zero, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Hour)
	defer cancel()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "unix", socket)
	}, MaxResponseHeaderBytes: 64 << 10, ResponseHeaderTimeout: time.Hour, DisableCompression: true, MaxConnsPerHost: 1}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, "POST", "http://benchd.local/v2/operations", bytes.NewReader(raw))
	if err != nil {
		return zero, err
	}
	req.Header.Set("Content-Type", contentType)
	response, err := (&http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return zero, fmt.Errorf("workspace socket request interrupted; reconcile using its request id: %w", err)
	}
	defer response.Body.Close()
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return zero, errors.New("workspace service response is not JSON")
	}
	limit := protocolv2.ResponseLimit(request)
	body, err := io.ReadAll(io.LimitReader(response.Body, int64(limit)+1))
	if err != nil {
		return zero, fmt.Errorf("workspace response interrupted; reconcile using its request id: %w", err)
	}
	if len(body) > limit {
		return zero, errors.New("workspace service response exceeds limit")
	}
	parsed, err := protocolv2.ParseResponse(body, response.StatusCode, request)
	if err != nil {
		return zero, err
	}
	return parsed, ErrorV2(parsed)
}
