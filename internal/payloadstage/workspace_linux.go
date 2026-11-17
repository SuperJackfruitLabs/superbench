//go:build linux

package payloadstage

import (
	"context"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadlaunch"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/serviceidentity"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/stageexec"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
)

type transport struct {
	*payloadlaunch.Bridge
	guard payloadlaunch.CommandGuard
	check func() error
}

func preparedTransport(bridge *payloadlaunch.Bridge, cmd *exec.Cmd, check func() error) *transport {
	return &transport{Bridge: bridge, guard: payloadlaunch.GuardCommand(cmd), check: check}
}

func (t *transport) Check() error {
	if err := t.guard.Check(); err != nil {
		return errors.Join(ErrRegistration, err)
	}
	return t.check()
}
func (t *transport) Drain(ctx context.Context, backend interface {
	Fence(*execution.Execution) error
}) error {
	return t.Bridge.Drain(ctx, backend)
}

func workspace(authority serviceidentity.Authority, b *model.Bench, member string, root bool, input []byte, finite bool) stageexec.PrepareTransport {
	return func(cmd *exec.Cmd, e *execution.Execution) (stageexec.Transport, error) {
		policy, err := authority.Policy()
		if err != nil {
			return nil, err
		}
		expectedRoot := filepath.Join(policy.DataRoot, "benches", b.Key)
		scalar := b.Workspace == nil
		expectedPath := expectedRoot
		layoutID := b.RegistrationID
		if scalar {
			expectedPath = filepath.Join(expectedRoot, "worktree")
			if b.AllocationRoot != expectedRoot {
				return nil, ErrRegistration
			}
		} else {
			layoutID = b.Workspace.ID
		}
		if cmd == nil || e == nil || !reflect.DeepEqual(*e, *b.Lease.Execution) || e.Root != policy.CgroupRoot || e.Log != filepath.Join(policy.StateRoot, "run-"+e.ID+".log") || b.Path != expectedPath {
			return nil, ErrRegistration
		}
		expectedDir := expectedRoot
		if scalar {
			expectedDir = expectedPath
		} else if !root {
			expectedDir = filepath.Join(expectedRoot, member)
		}
		if cmd.Dir != expectedDir {
			return nil, ErrRegistration
		}
		// Existing callers carry explicit cache selection. The helper has no typed
		// cache-mode envelope yet; never silently discard these product settings.
		for _, line := range cmd.Env {
			key, _, _ := strings.Cut(line, "=")
			if strings.HasPrefix(strings.ToLower(key), "npm_config_") || key == "BUN_INSTALL_CACHE_DIR" || key == "PLAYWRIGHT_BROWSERS_PATH" || key == "GOCACHE" || key == "GOMODCACHE" {
				return nil, ErrCache
			}
		}
		request := payloadexec.Request{Version: payloadexec.Version, Key: b.Key, LayoutID: layoutID, Member: member, Root: root, Kind: "payload", Scope: payloadexec.Scope{ID: e.ID, HostID: e.HostID, BootID: e.BootID}, Arguments: append([]string(nil), cmd.Args...)}
		registry, err := serviceidentity.ForPolicy(policy)
		if err != nil {
			return nil, err
		}
		request.PolicyDigest = registry.PolicySHA256
		layout, err := payloadexec.LoadLayout(policy, request)
		if err != nil {
			return nil, errors.Join(ErrRegistration, err)
		}
		if layout.Scalar != scalar {
			return nil, ErrRegistration
		}
		if !scalar && len(layout.Members) != len(b.Workspace.Members) {
			return nil, ErrRegistration
		}
		found := root || scalar
		var members []model.WorkspaceMember
		if !scalar {
			members = b.Workspace.Members
		}
		for i, m := range members {
			if layout.Members[i].Name != m.Name || m.Path != filepath.Join(expectedRoot, m.Name) {
				return nil, ErrRegistration
			}
			found = found || m.Name == member
		}
		if !found {
			return nil, ErrRegistration
		}
		target := payloadlaunch.Target{Key: request.Key, LayoutID: request.LayoutID, Member: member, Root: root, PolicyDigest: request.PolicyDigest, Kind: "payload"}
		var bridge *payloadlaunch.Bridge
		if finite {
			// Copied bytes reach the helper frame only; it seals them in a memfd
			// and binds stdin once after the credential drop. Empty keeps EOF.
			bridge, err = payloadlaunch.PreparePayloadInput(cmd, target, e, payloadlaunch.PayloadInput{Bytes: input, Limit: payloadexec.MaxPayloadInput, ReportExec: true})
		} else {
			bridge, err = payloadlaunch.Prepare(cmd, target, e, payloadlaunch.GitInput{})
		}
		if err != nil {
			return nil, err
		}
		return preparedTransport(bridge, cmd, func() error {
			current, err := authority.Policy()
			if err != nil {
				return err
			}
			if current != policy {
				return ErrRegistration
			}
			now, err := payloadexec.LoadLayout(current, request)
			if err != nil || !reflect.DeepEqual(now, layout) {
				return errors.Join(ErrRegistration, err)
			}
			return nil
		}), nil
	}
}
