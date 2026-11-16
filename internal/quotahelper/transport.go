// Package quotahelper invokes the fixed-policy helper without changing its
// logical assignment semantics. It never chooses arbitrary filesystem limits.
package quotahelper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"os"
	"os/exec"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotapolicy"
	"path/filepath"
)

type Assignment = quotapolicy.Assignment
type Config struct{ State, Data, Command, PolicyOverride string }

func Apply(ctx context.Context, c Config, kind, key string, requested uint64) (Assignment, error) {
	if _, scoped := operationcontrol.FromContext(ctx); scoped {
		return applyScoped(ctx, c, kind, key, requested, os.Geteuid(), 0)
	}
	return apply(ctx, c, kind, key, requested, os.Geteuid())
}
func apply(ctx context.Context, c Config, kind, key string, requested uint64, uid int) (Assignment, error) {
	args := []string{}
	if c.PolicyOverride != "" {
		args = append(args, "--policy", c.PolicyOverride)
	}
	args = append(args, "apply", kind, key)
	if requested != 0 {
		args = append(args, fmt.Sprint(requested))
	}
	command := c.Command
	if uid != 0 {
		args = append([]string{"-n", command}, args...)
		command = "/usr/bin/sudo"
	}
	cmd := exec.Command(command, args...)
	stdout, stderr := &boundedOutput{max: MaxReply}, &boundedOutput{max: 4096}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	e := operationcontrol.Effect(ctx, cmd.Start)
	if e == nil {
		e = cmd.Wait()
	}
	if e != nil {
		var exit *exec.ExitError
		if !stdout.overflow && !stderr.overflow && stdout.Len() == 0 && errors.As(e, &exit) && exit.ExitCode() == 1 {
			if capacity := capacityFailure(stderr.Bytes()); capacity != nil {
				return Assignment{}, fmt.Errorf("quota allocation: %w: %w", e, capacity)
			}
		}
		if stdout.overflow || stderr.overflow {
			return Assignment{}, fmt.Errorf("quota allocation: %w: helper output exceeds bounds", e)
		}
		return Assignment{}, fmt.Errorf("quota allocation: %w: %s%s", e, stdout.Bytes(), stderr.Bytes())
	}
	if stdout.overflow || stderr.overflow {
		return Assignment{}, errors.New("quota helper output exceeds bounds")
	}
	if stderr.Len() != 0 {
		return Assignment{}, errors.New("quota helper returned ambiguous output")
	}
	raw := stdout.Bytes()
	var a Assignment
	if e = json.Unmarshal(raw, &a); e != nil {
		return a, e
	}
	expected := filepath.Join(c.Data, key)
	if kind == "bench" {
		expected = filepath.Join(c.Data, "benches", key)
	}
	if a.Path != expected || a.ProjectID == 0 || a.Bytes == 0 || a.Inodes == 0 || requested != 0 && a.Bytes != requested {
		return a, errors.New("quota helper returned a conflicting assignment")
	}
	return a, nil
}
