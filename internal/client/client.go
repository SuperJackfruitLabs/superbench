// Package client talks only to a local Unix socket, never an environment proxy.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocol"
)

func Call(ctx context.Context, socket string, request protocol.Request) (json.RawMessage, error) {
	raw, e := json.Marshal(request)
	if e != nil {
		return nil, e
	}
	if _, e = protocol.Parse(raw); e != nil {
		return nil, e
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	req, e := http.NewRequestWithContext(ctx, "POST", "http://benchd.local/v1/operations", bytes.NewReader(raw))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Content-Type", "application/json")
	response, e := (&http.Client{Transport: transport}).Do(req)
	if e != nil {
		return nil, fmt.Errorf("socket request interrupted; reconcile using its request id: %w", e)
	}
	defer response.Body.Close()
	body, e := io.ReadAll(io.LimitReader(response.Body, protocol.MaxResponse+1))
	if e != nil {
		return nil, e
	}
	if len(body) > protocol.MaxResponse {
		return nil, errors.New("service response exceeds limit")
	}
	var result struct {
		Version, Operation string
		RequestID          string `json:"request_id"`
		OK                 bool
		Result             json.RawMessage
		Error              *protocol.APIError
	}
	if e = json.Unmarshal(body, &result); e != nil {
		return nil, e
	}
	// Failures before request parsing cannot carry a trusted operation identity.
	earlyFailure := !result.OK && result.Operation == "" && result.RequestID == "" && len(result.Result) == 0 && result.Error != nil &&
		((response.StatusCode == 403 && result.Error.Code == "forbidden") ||
			((response.StatusCode == 400 || response.StatusCode == 413 || response.StatusCode == 415) && result.Error.Code == "invalid_request"))
	if result.Version != protocol.Version || !earlyFailure && (result.Operation != request.Operation || result.RequestID != request.RequestID) {
		return nil, errors.New("service response identity mismatch")
	}
	if !result.OK {
		if result.Error == nil {
			return nil, errors.New("service returned an invalid failure response")
		}
		var failed struct {
			ExitCode int `json:"exit_code"`
		}
		_ = json.Unmarshal(result.Result, &failed)
		result.Error.CommandExit = failed.ExitCode
		return result.Result, result.Error
	}
	if response.StatusCode != 200 || result.Error != nil {
		return nil, errors.New("service returned an inconsistent response")
	}
	return result.Result, nil
}
