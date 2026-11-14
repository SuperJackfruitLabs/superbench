package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/client"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocol"
)

func remote(ctx context.Context, socket string, r protocol.Request) error {
	r.Version = protocol.Version
	if protocol.Growth(r.Operation) && r.RequestID == "" {
		nonce := make([]byte, 16)
		if _, e := rand.Read(nonce); e != nil {
			return e
		}
		r.RequestID = hex.EncodeToString(nonce)
	}
	if r.RequestID != "" {
		fmt.Fprintln(os.Stderr, "request id:", r.RequestID)
	}
	result, e := client.Call(ctx, socket, r)
	if len(result) > 0 && string(result) != "null" {
		if !json.Valid(result) {
			return fmt.Errorf("invalid result JSON")
		}
		if _, writeErr := os.Stdout.Write(append(result, '\n')); writeErr != nil {
			return writeErr
		}
	}
	return e
}
