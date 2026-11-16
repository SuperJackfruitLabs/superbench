//go:build linux

package registrationobserve

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"os/exec"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
)

const helper = "/usr/local/libexec/bench-payload"

type boundedOutput struct {
	bytes.Buffer
	limit int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, ErrObservation
	}
	return b.Buffer.Write(p)
}

func inspectionCommand(ctx context.Context, raw []byte) (*exec.Cmd, *boundedOutput) {
	frame := make([]byte, 4+len(raw))
	binary.BigEndian.PutUint32(frame, uint32(len(raw)))
	copy(frame[4:], raw)
	// Go's child stdio transport creates daemon-owned pipes, required by the
	// actual helper. No inherited descriptor, terminal, file or ambient env.
	cmd := exec.CommandContext(ctx, "/usr/bin/sudo", "-n", "--", helper)
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	cmd.Stdin = bytes.NewReader(frame)
	out := &boundedOutput{limit: payloadexec.MaxLayout + 4}
	cmd.Stdout = out
	cmd.Stderr = &boundedOutput{limit: 2048}
	cmd.WaitDelay = time.Second
	return cmd, out
}

func inspect(ctx context.Context, p payloadpolicy.Policy, r payloadexec.InspectionRequest) (payloadexec.Layout, error) {
	raw, err := r.Bytes()
	if err != nil {
		return payloadexec.Layout{}, ErrObservation
	}
	cmd, out := inspectionCommand(ctx, raw)
	if err = cmd.Run(); err != nil || ctx.Err() != nil {
		return payloadexec.Layout{}, ErrObservation
	}
	return inspectionResult(p, r, out.Bytes())
}

func inspectionResult(p payloadpolicy.Policy, r payloadexec.InspectionRequest, frame []byte) (payloadexec.Layout, error) {
	if len(frame) < 4 || len(frame) > payloadexec.MaxLayout+4 || int(binary.BigEndian.Uint32(frame[:4])) != len(frame)-4 {
		return payloadexec.Layout{}, ErrObservation
	}
	l, err := payloadexec.ParseLayout(p, frame[4:])
	if err != nil || l.Scalar || l.Key != r.Key || l.ID != r.LayoutID || l.PolicyDigest != r.PolicyDigest {
		return payloadexec.Layout{}, ErrObservation
	}
	return l, nil
}

var _ io.Writer = (*boundedOutput)(nil)
