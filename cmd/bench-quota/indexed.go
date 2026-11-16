package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperationcontrol"
)

var errUnscopedIndexed = errors.New("indexed operation policy requires original typed operation identity; unscoped apply/release refused")

func checkIndexedCommand(p hoststorage.Policy, command string) error {
	if p.OperationJournal != "" && (command == "apply" || command == "release") {
		return errUnscopedIndexed
	}
	return nil
}

// Generic indexed helper actions share the operation barrier. Output is kept
// private until the final authority checks pass, including for read-only calls.
func runFixedPolicy(ctx context.Context, path string, p hoststorage.Policy, effect func(io.Writer) error, output io.Writer) error {
	if p.OperationJournal == "" {
		return effect(output)
	}
	if p.OperationJournal != "indexed-v2" || path != quotaoperationcontrol.PolicyPath {
		return quotaoperationcontrol.ErrPolicy
	}
	c, installed, err := quotaoperationcontrol.Open(ctx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(p, installed) {
		return quotaoperationcontrol.ErrPolicy
	}
	return bufferedQuotaOutput(func(fn func() error) error { return c.Run(ctx, fn) }, effect, output)
}

// Publication is separate from effect completion: the retained control performs
// final authority checks after the effect returns and before these bytes escape.
func bufferedQuotaOutput(run func(func() error) error, effect func(io.Writer) error, output io.Writer) error {
	var pending quotaOutput
	if err := run(func() error { return effect(&pending) }); err != nil {
		return err
	}
	return writeQuotaOutput(output, pending.buffer.Bytes())
}

func runIndexedOperation(ctx context.Context, p hoststorage.Policy, r operationRequest, binary, authority string, output io.Writer) error {
	c, installed, err := quotaoperationcontrol.Open(ctx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(p, installed) {
		return quotaoperationcontrol.ErrPolicy
	}
	var result any
	if r.command == "operation-info" {
		err = c.Run(ctx, func() error {
			result = quotahelper.Info{Version: quotaoperation.Version, Root: p.Root, Authority: authority, BinarySHA256: binary}
			return nil
		})
	} else {
		s, openErr := c.Operation(ctx, r.identity, func(i quotaoperation.Identity) error {
			current, err := p.OperationAuthority(ctx)
			if err != nil {
				return err
			}
			actual, err := quotahelper.SelfBinarySHA256()
			if err != nil {
				return err
			}
			if i != r.identity || current != i.Authority || actual != i.BinarySHA256 {
				return quotaoperation.ErrAuthority
			}
			return nil
		})
		if openErr != nil {
			return openErr
		}
		if r.command == "cancel-operation" {
			result, err = s.Cancel(ctx)
		} else {
			var assignment hoststorage.Assignment
			err = s.Apply(ctx, func() error {
				var applyErr error
				assignment, applyErr = p.ApplyRequested(r.kind, r.key, r.requested)
				return applyErr
			})
			result = quotahelper.ApplyReply{Version: quotaoperation.Version, Identity: r.identity, Assignment: assignment}
		}
	}
	if err != nil {
		return err
	}
	return operationOutput(output, result)
}

type quotaOutput struct{ buffer bytes.Buffer }

func (b *quotaOutput) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > quotahelper.MaxReply {
		return 0, errors.New("quota output exceeds limit")
	}
	return b.buffer.Write(p)
}

func writeQuotaOutput(w io.Writer, raw []byte) error {
	n, err := w.Write(raw)
	if err == nil && n != len(raw) {
		return io.ErrShortWrite
	}
	return err
}
