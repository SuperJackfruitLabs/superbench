package storageproof

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

type storageOutput struct {
	buffer   bytes.Buffer
	overflow bool
	stop     context.CancelFunc
}

func (b *storageOutput) Write(raw []byte) (int, error) {
	if len(raw) > MaxIdentity-b.buffer.Len() {
		b.overflow = true
		if b.stop != nil {
			b.stop()
		}
		return 0, errors.New("storage command output exceeds limit")
	}
	return b.buffer.Write(raw)
}

func VerificationCommand(ctx context.Context, binary string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	var stdout, stderr storageOutput
	stdout.stop, stderr.stop = cancel, cancel
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if stdout.overflow || stderr.overflow {
		return nil, errors.New("storage command output exceeds limit")
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("read-only storage verification command failed")
	}
	return stdout.buffer.Bytes(), nil
}

func RetirementCommand(ctx context.Context, input []byte, binary string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	cmd.Stdin = bytes.NewReader(input)
	cmd.WaitDelay = time.Second
	var stdout, stderr storageOutput
	stdout.stop, stderr.stop = cancel, cancel
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if stdout.overflow || stderr.overflow {
		return nil, errors.New("retirement helper output exceeds limit")
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("approved retirement helper failed")
	}
	return stdout.buffer.Bytes(), nil
}
