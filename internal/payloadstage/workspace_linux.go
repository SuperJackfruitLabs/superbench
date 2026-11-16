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
	check func() error
}

func (t *transport) Check() error { return t.check() }
func (t *transport) Drain(ctx context.Context, backend interface {
	Fence(*execution.Execution) error
}) error {
	return t.Bridge.Drain(ctx, backend)
}

func workspace(authority serviceidentity.Authority, b *model.Bench, member string, root bool) stageexec.PrepareTransport {
	return func(cmd *exec.Cmd, e *execution.Execution) (stageexec.Transport, error) {
		policy, err := authority.Policy()
		if err != nil {
			return nil, err
		}
		expectedRoot := filepath.Join(policy.DataRoot, "benches", b.Key)
		if cmd == nil || e == nil || !reflect.DeepEqual(*e, *b.Lease.Execution) || e.Root != policy.CgroupRoot || e.Log != filepath.Join(policy.StateRoot, "run-"+e.ID+".log") || b.Path != expectedRoot {
			return nil, ErrRegistration
		}
		expectedDir := expectedRoot
		if !root {
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
		request := payloadexec.Request{Version: payloadexec.Version, Key: b.Key, LayoutID: b.Workspace.ID, Member: member, Root: root, Kind: "payload", Scope: payloadexec.Scope{ID: e.ID, HostID: e.HostID, BootID: e.BootID}, Arguments: append([]string(nil), cmd.Args...)}
		registry, err := serviceidentity.ForPolicy(policy)
		if err != nil {
			return nil, err
		}
		request.PolicyDigest = registry.PolicySHA256
		layout, err := payloadexec.LoadLayout(policy, request)
		if err != nil {
			return nil, errors.Join(ErrRegistration, err)
		}
		if layout.Scalar || len(layout.Members) != len(b.Workspace.Members) {
			return nil, ErrRegistration
		}
		found := root
		for i, m := range b.Workspace.Members {
			if layout.Members[i].Name != m.Name || m.Path != filepath.Join(expectedRoot, m.Name) {
				return nil, ErrRegistration
			}
			found = found || m.Name == member
		}
		if !found {
			return nil, ErrRegistration
		}
		bridge, err := payloadlaunch.Prepare(cmd, payloadlaunch.Target{Key: request.Key, LayoutID: request.LayoutID, Member: member, Root: root, PolicyDigest: request.PolicyDigest, Kind: "payload"}, e, payloadlaunch.GitInput{})
		if err != nil {
			return nil, err
		}
		return &transport{Bridge: bridge, check: func() error {
			current, err := authority.Policy()
			if err != nil {
				return err
			}
			if current != policy || cmd.Path != "/usr/bin/sudo" || !reflect.DeepEqual(cmd.Args, []string{"sudo", "-n", "--", payloadlaunch.Helper}) || cmd.Dir != "/" || len(cmd.ExtraFiles) != 0 || cmd.SysProcAttr != nil {
				return ErrRegistration
			}
			now, err := payloadexec.LoadLayout(current, request)
			if err != nil || !reflect.DeepEqual(now, layout) {
				return errors.Join(ErrRegistration, err)
			}
			return nil
		}}, nil
	}
}
