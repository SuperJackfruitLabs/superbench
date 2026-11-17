//go:build linux

package freshtree

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadgit"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadlaunch"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadstage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/registeredinspection"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/serviceidentity"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/stageexec"
)

const maxObservation = 1 << 20

// installed binds a registered workspace to the installed service identity.
// Any failure is a refusal; a registered workspace is never treated as legacy.
func installed(b *model.Bench) (Tree, error) {
	authority, err := serviceidentity.ResolveService()
	if err != nil {
		return nil, err
	}
	p, err := authority.Policy()
	if err != nil {
		return nil, err
	}
	if b.Path != filepath.Join(p.DataRoot, "benches", b.Key) {
		return nil, ErrHelper
	}
	registry, err := serviceidentity.ForPolicy(p)
	if err != nil {
		return nil, err
	}
	return helperTree{authority: authority, policy: p, digest: registry.PolicySHA256}, nil
}

type helperTree struct {
	authority serviceidentity.Authority
	policy    payloadpolicy.Policy
	digest    string
}

type bounded struct {
	bytes.Buffer
	limit int
}

func (b *bounded) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, ErrHelper
	}
	return b.Buffer.Write(p)
}

// preparation asks the helper, as root and read-only, for one member's fixed
// inputs and the registered layout, then binds the reply to b's exact vector.
func (t helperTree) preparation(ctx context.Context, b *model.Bench, member string) (registeredinspection.Preparation, error) {
	var zero registeredinspection.Preparation
	if err := t.current(b); err != nil {
		return zero, err
	}
	raw, err := (registeredinspection.PreparationRequest{Version: registeredinspection.PreparationVersion, Key: b.Key, LayoutID: b.Workspace.ID, PolicyDigest: t.digest, Member: member}).Bytes()
	if err != nil {
		return zero, errors.Join(ErrHelper, err)
	}
	frame := make([]byte, 4+len(raw))
	binary.BigEndian.PutUint32(frame, uint32(len(raw)))
	copy(frame[4:], raw)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Daemon-owned pipes only: no inherited descriptor, terminal or ambient env.
	cmd := exec.CommandContext(ctx, "/usr/bin/sudo", "-n", "--", payloadlaunch.Helper)
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	cmd.Stdin = bytes.NewReader(frame)
	out := &bounded{limit: registeredinspection.MaxPreparation + 4}
	cmd.Stdout, cmd.Stderr = out, &bounded{limit: 2048}
	cmd.WaitDelay = time.Second
	if err = cmd.Run(); err != nil || ctx.Err() != nil {
		return zero, errors.Join(ErrHelper, err)
	}
	v, err := bindPreparation(t.policy, t.digest, b, member, out.Bytes())
	if err != nil {
		return zero, err
	}
	return v, t.current(b)
}

// current rechecks the installed identity and the record's fixed location.
func (t helperTree) current(b *model.Bench) error {
	p, err := t.authority.Policy()
	if err != nil || p != t.policy || b == nil || b.Workspace == nil || b.Path != filepath.Join(p.DataRoot, "benches", b.Key) {
		return errors.Join(ErrHelper, err)
	}
	return nil
}

func (t helperTree) Root(ctx context.Context, b *model.Bench, clean bool) error {
	v, err := t.preparation(ctx, b, b.Workspace.CheckpointRepository)
	if err != nil {
		return err
	}
	return rootResult(v, clean)
}

func (t helperTree) Inputs(ctx context.Context, b *model.Bench, member string) (registeredinspection.Inputs, error) {
	v, err := t.preparation(ctx, b, member)
	return v.Inputs, err
}

func (t helperTree) runner() gitcontrol.Runner {
	return gitcontrol.Runner{State: t.policy.StateRoot, Root: t.policy.CgroupRoot}
}

func (t helperTree) Git(ctx context.Context, b *model.Bench, member string, args ...string) (string, error) {
	if err := t.current(b); err != nil {
		return "", err
	}
	route, err := payloadgit.Observe(t.authority, b, member)
	if err != nil {
		return "", err
	}
	out, err := route.Execute(ctx, t.runner(), args, nil, 0, maxObservation)
	return string(out), err
}

func (t helperTree) Transport(b *model.Bench, member string) (stageexec.PrepareTransport, error) {
	if err := t.current(b); err != nil {
		return nil, err
	}
	return payloadstage.Workspace(t.authority, b, member, member == "")
}

func (t helperTree) Export(ctx context.Context, b *model.Bench, member string, exclude []string) ([]byte, error) {
	if err := t.current(b); err != nil {
		return nil, err
	}
	route, err := payloadgit.New(t.authority, b, member)
	if err != nil {
		return nil, err
	}
	args := []string{"bundle", "create", "-", "HEAD"}
	if len(exclude) != 0 {
		args = append(append(args, "--not"), exclude...)
	}
	raw, err := route.Execute(ctx, t.runner(), args, nil, 0, MaxExport)
	if errors.Is(err, io.ErrShortBuffer) {
		return nil, fmt.Errorf("%w: member %s", ErrExportCap, member)
	}
	return raw, err
}
