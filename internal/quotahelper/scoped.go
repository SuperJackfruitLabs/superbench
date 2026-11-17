package quotahelper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

type boundedOutput struct {
	mu sync.Mutex
	bytes.Buffer
	max      int
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(p) > b.max-b.Len() {
		b.overflow = true
		return 0, io.ErrShortWrite
	}
	return b.Buffer.Write(p)
}
func helperArgs(c Config, uid int, args []string) (string, []string) {
	if c.PolicyOverride != "" {
		args = append([]string{"--policy", c.PolicyOverride}, args...)
	}
	if uid != 0 {
		return "/usr/bin/sudo", append([]string{"-n", c.Command}, args...)
	}
	return c.Command, args
}
func helperCommand(ctx context.Context, c Config, uid int, guard bool, beforeStart func() error, args ...string) ([]byte, error) {
	return helperCommandInput(ctx, c, uid, guard, beforeStart, nil, args...)
}

// Input is a finite owned byte slice; no caller reader can hold a privileged
// helper open indefinitely. The helper parses it before acquiring its lock.
func helperCommandInput(ctx context.Context, c Config, uid int, guard bool, beforeStart func() error, input []byte, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	capacityEligible := len(args) > 0 && args[0] == "apply-operation"
	command, args := helperArgs(c, uid, args)
	cmd := exec.Command(command, args...)
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	stdout, stderr := &boundedOutput{max: MaxReply}, &boundedOutput{max: 4096}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	start := func() error {
		if beforeStart != nil {
			if err := beforeStart(); err != nil {
				return err
			}
		}
		return cmd.Start()
	}
	var err error
	if guard {
		err = operationcontrol.Effect(ctx, start)
	} else {
		err = start()
	}
	if err == nil {
		err = cmd.Wait()
	}
	if err != nil {
		var exit *exec.ExitError
		if capacityEligible && !stdout.overflow && !stderr.overflow && stdout.Len() == 0 && errors.As(err, &exit) && exit.ExitCode() == 1 {
			if capacity := capacityFailure(stderr.Bytes()); capacity != nil {
				return nil, fmt.Errorf("fixed-helper operation failed: %w: %w", err, capacity)
			}
		}
		if stderr.overflow {
			return nil, fmt.Errorf("fixed-helper operation failed: %w: helper output exceeds bounds", err)
		}
		// The helper's own refusal line is the cause benchd logs; it never
		// reaches a client, and the helper holds no credentials or payload data.
		return nil, fmt.Errorf("fixed-helper operation failed: %w: %s", err, helperDiagnostic(stderr.Bytes()))
	}
	if stdout.overflow || stderr.overflow {
		return nil, errors.New("fixed-helper output exceeds bounds")
	}
	return stdout.Bytes(), nil
}
func stateIdentity(path string) (string, uint64, uint64, error) {
	state, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", 0, 0, err
	}
	info, err := os.Lstat(state)
	if err != nil {
		return "", 0, 0, err
	}
	if !info.IsDir() || info.Mode().Perm()&0022 != 0 {
		return "", 0, 0, errors.New("helper intent requires protected physical state root")
	}
	st := info.Sys().(*syscall.Stat_t)
	return state, uint64(st.Dev), st.Ino, nil
}
func applyScoped(ctx context.Context, c Config, kind, key string, requested uint64, uid int, trustedUID uint32) (Assignment, error) {
	var zero Assignment
	if err := operationcontrol.Check(ctx); err != nil {
		return zero, err
	}
	i, ok := operationcontrol.FromContext(ctx)
	if !ok {
		return zero, operationcontrol.ErrAuthority
	}
	binary, err := installedBinary(c.Command, trustedUID)
	if err != nil {
		return zero, err
	}
	state, device, inode, err := stateIdentity(c.State)
	if err != nil {
		return zero, err
	}
	raw, err := helperCommand(ctx, c, uid, true, nil, "operation-info")
	if err != nil {
		return zero, err
	}
	var info Info
	if err = strictReply(raw, &info); err != nil {
		return zero, err
	}
	if !validInfo(info) || info.Root != c.Data || info.BinarySHA256 != binary {
		return zero, operationcontrol.ErrAuthority
	}
	policy := c.PolicyOverride
	if policy == "" {
		policy = "/etc/superbench/storage.json"
	}
	h := operationcontrol.Helper{Identity: quotaoperation.Identity{OperationID: i.Target.ID(), Epoch: i.Epoch, Authority: info.Authority, BinarySHA256: binary}, Command: c.Command, Policy: policy, State: state, StateDevice: device, StateInode: inode, Kind: kind, Key: key, Requested: requested}
	h.Identity.StateIdentity = h.StateIdentity()
	if err = operationcontrol.RecordHelper(ctx, h); err != nil {
		return zero, err
	}
	recheckState := func() error {
		named, dev, ino, err := stateIdentity(c.State)
		if err != nil {
			return err
		}
		if named != h.State || dev != h.StateDevice || ino != h.StateInode {
			return operationcontrol.ErrAuthority
		}
		return nil
	}
	raw, err = helperCommand(ctx, c, uid, true, recheckState, "apply-operation", kind, key, strconv.FormatUint(requested, 10), h.Identity.OperationID, h.Identity.Epoch, h.Identity.StateIdentity, h.Identity.Authority, h.Identity.BinarySHA256)
	if err != nil {
		return zero, err
	}
	var reply ApplyReply
	if err = strictReply(raw, &reply); err != nil {
		return zero, err
	}
	a := reply.Assignment
	expected := filepath.Join(c.Data, key)
	if kind == "bench" {
		expected = filepath.Join(c.Data, "benches", key)
	}
	if reply.Version != quotaoperation.Version || reply.Identity != h.Identity || a.Path != expected || a.ProjectID == 0 || a.Bytes == 0 || a.Inodes == 0 || requested != 0 && a.Bytes != requested {
		return zero, operationcontrol.ErrAuthority
	}
	return a, nil
}

// helperDiagnostic is the first stderr line, bounded and quoted.
func helperDiagnostic(stderr []byte) string {
	line, _, _ := bytes.Cut(stderr, []byte("\n"))
	if len(line) > 512 {
		line = line[:512]
	}
	return strconv.Quote(string(line))
}
