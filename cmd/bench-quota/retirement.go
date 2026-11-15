package main

import (
	"context"
	"encoding/json"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"io"
)

func runRetirement(p hoststorage.Policy, operation, key string, input io.Reader, output io.Writer) error {
	raw, err := io.ReadAll(io.LimitReader(input, hoststorage.MaxRetirementApproval+1))
	if err != nil {
		return err
	}
	approval, err := hoststorage.ParseRetirementApproval(raw)
	if err != nil {
		return err
	}
	if approval.Key != key {
		return errors.New("retirement approval belongs to another logical bench")
	}
	var assignment hoststorage.Assignment
	switch operation {
	case "retire-approved":
		assignment, err = p.RetireApproved(context.Background(), approval)
	case "verify-retirement-authority":
		assignment, err = p.VerifyRetirementAuthority(context.Background(), approval)
	case "verify-retired":
		assignment, err = p.VerifyRetiredStorage(context.Background(), approval)
	default:
		return errors.New("invalid fixed retirement operation")
	}
	if err != nil {
		return err
	}
	raw, err = json.Marshal(assignment)
	if err != nil {
		return err
	}
	if len(raw) > hoststorage.MaxStorageIdentity {
		return errors.New("retirement output exceeds limit")
	}
	_, err = output.Write(raw)
	return err
}
