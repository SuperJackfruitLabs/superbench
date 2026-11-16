package hoststorage

import (
	"bytes"
	"context"

	"encoding/json"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storageproof"
	"os"
)

func (w Worker) RetireStorage(ctx context.Context, a RetirementApproval) error {
	return w.retirement(ctx, a, false, retirementCommand)
}
func (w Worker) VerifyRetiredStorage(ctx context.Context, a RetirementApproval) error {
	return w.retirement(ctx, a, true, retirementCommand)
}

// VerifyRetirementAuthority is read-only. A successful result grants NO current
// leaf identity, absence, fencing or deletion authority to its caller.
func (w Worker) VerifyRetirementAuthority(ctx context.Context, a RetirementApproval) error {
	return w.retirementOperation(ctx, a, "verify-retirement-authority", retirementCommand)
}

func (w Worker) retirement(ctx context.Context, a RetirementApproval, verifyOnly bool, run func(context.Context, []byte, string, ...string) ([]byte, error)) error {
	operation := "retire-approved"
	if verifyOnly {
		operation = "verify-retired"
	}
	return w.retirementOperation(ctx, a, operation, run)
}
func (w Worker) retirementOperation(ctx context.Context, a RetirementApproval, operation string, run func(context.Context, []byte, string, ...string) ([]byte, error)) error {
	authorityOnly := operation == "verify-retirement-authority"
	if !authorityOnly && operation != "retire-approved" && operation != "verify-retired" {
		return errors.New("invalid fixed retirement operation")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := a.Bytes()
	if err != nil {
		return err
	}
	if a.Storage.Root != w.Data {
		return errors.New("retirement belongs to another data root")
	}
	c, err := w.inspectStorageConfig()
	if err != nil {
		return err
	}
	if c == nil {
		return errors.New("approved managed retirement requires existing host policy")
	}
	config, _ := json.Marshal(c)
	if storageDigest(config) != a.Storage.ConfigSHA256 {
		return errors.New("retirement host policy changed")
	}
	parent, e := openRetirementParent(a.Storage, &a.Root)
	if e != nil {
		return e
	}
	if !authorityOnly {
		_, e = checkRetirementRoot(parent, a, true)
	}
	parent.Close()
	if e != nil {
		return e
	}
	args := []string{}
	if c.PolicyOverride != "" {
		args = append(args, "--policy", c.PolicyOverride)
	}
	args = append(args, operation, "bench", a.Key)
	binary := c.QuotaHelper
	if os.Geteuid() != 0 {
		args = append([]string{"-n", binary}, args...)
		binary = "/usr/bin/sudo"
	}
	output, err := run(ctx, raw, binary, args...)
	if err != nil {
		return err
	}
	if len(output) > MaxStorageIdentity {
		return errors.New("retirement result exceeds limit")
	}
	var actual Assignment
	if err = strictStorageJSON(output, &actual); err != nil {
		return err
	}
	canonical, _ := json.Marshal(actual)
	expected := a.Storage.Assignment
	expected.Bytes, expected.Inodes = 0, 0
	if (actual != expected && (!authorityOnly || actual != a.Storage.Assignment)) || !bytes.Equal(output, canonical) {
		return errors.New("retirement helper returned different authority")
	}
	parent, e = openRetirementParent(a.Storage, &a.Root)
	if e != nil {
		return e
	}
	if !authorityOnly {
		_, e = checkRetirementRoot(parent, a, true)
	}
	parent.Close()
	if e != nil {
		return e
	}
	return ctx.Err()
}
func retirementCommand(ctx context.Context, input []byte, binary string, args ...string) ([]byte, error) {
	return storageproof.RetirementCommand(ctx, input, binary, args...)
}
