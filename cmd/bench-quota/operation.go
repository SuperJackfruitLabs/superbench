package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

type operationRequest struct {
	command, kind, key string
	requested          uint64
	identity           quotaoperation.Identity
}

func isOperationCommand(s string) bool {
	return s == "operation-info" || s == "apply-operation" || s == "cancel-operation"
}
func parseOperation(args []string) (operationRequest, error) {
	var r operationRequest
	invalid := errors.New("invalid quota operation command")
	if len(args) == 0 {
		return r, invalid
	}
	r.command = args[0]
	if r.command == "operation-info" {
		if len(args) != 1 {
			return r, invalid
		}
		return r, nil
	}
	offset := 1
	if r.command == "apply-operation" {
		if len(args) != 9 {
			return r, invalid
		}
		r.kind, r.key = args[1], args[2]
		n, err := strconv.ParseUint(args[3], 10, 64)
		if err != nil || strconv.FormatUint(n, 10) != args[3] || n > 1<<50 || (n != 0 && (n < 1024 || n%1024 != 0)) {
			return r, invalid
		}
		r.requested = n
		if r.kind == "bench" {
			if !hoststorage.Key.MatchString(r.key) {
				return r, invalid
			}
		} else if r.kind != "shared" || (r.key != "store" && r.key != "mirrors") || n != 0 {
			return r, invalid
		}
		offset = 4
	} else if r.command != "cancel-operation" || len(args) != 6 {
		return r, invalid
	}
	r.identity = quotaoperation.Identity{OperationID: args[offset], Epoch: args[offset+1], StateIdentity: args[offset+2], Authority: args[offset+3], BinarySHA256: args[offset+4]}
	if !r.identity.Valid() {
		return r, invalid
	}
	return r, nil
}
func operationOutput(w io.Writer, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(raw) > quotahelper.MaxReply {
		return errors.New("quota operation reply exceeds limit")
	}
	n, err := w.Write(raw)
	if err == nil && n != len(raw) {
		return io.ErrShortWrite
	}
	return err
}
func runOperation(ctx context.Context, p hoststorage.Policy, r operationRequest, w io.Writer) error {
	authority, err := p.OperationAuthority(ctx)
	if err != nil {
		return err
	}
	binary, err := quotahelper.SelfBinarySHA256()
	if err != nil {
		return err
	}
	if r.command == "operation-info" {
		return operationOutput(w, quotahelper.Info{Version: quotaoperation.Version, Root: p.Root, Authority: authority, BinarySHA256: binary})
	}
	if r.identity.Authority != authority || r.identity.BinarySHA256 != binary {
		return quotaoperation.ErrAuthority
	}
	// The entire fixed-policy apply, including its directory creation before the
	// allocation ledger lock, is causally inside the durable operation barrier.
	if r.command == "apply-operation" && r.requested > p.BenchBytes {
		return errors.New("requested bench bytes exceed fixed policy")
	}
	store := quotaoperation.Store{Directory: p.Ledger, Check: func(i quotaoperation.Identity) error {
		current, e := p.OperationAuthority(ctx)
		if e != nil {
			return e
		}
		actual, e := quotahelper.SelfBinarySHA256()
		if e != nil {
			return e
		}
		if current != i.Authority || actual != i.BinarySHA256 {
			return quotaoperation.ErrAuthority
		}
		return nil
	}}
	if r.command == "cancel-operation" {
		proof, e := store.Cancel(r.identity)
		if e != nil {
			return e
		}
		return operationOutput(w, proof)
	}
	var assignment hoststorage.Assignment
	err = store.Apply(r.identity, func() error { var e error; assignment, e = p.ApplyRequested(r.kind, r.key, r.requested); return e })
	if err != nil {
		return err
	}
	return operationOutput(w, quotahelper.ApplyReply{Version: quotaoperation.Version, Identity: r.identity, Assignment: assignment})
}
