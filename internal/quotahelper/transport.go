// Package quotahelper invokes the fixed-policy helper without changing its
// logical assignment semantics. It never chooses arbitrary filesystem limits.
package quotahelper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"os"
	"os/exec"
	"path/filepath"
)

type Assignment struct {
	Path          string
	ProjectID     uint32
	Bytes, Inodes uint64
}
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
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	e := operationcontrol.Effect(ctx, cmd.Start)
	if e == nil {
		e = cmd.Wait()
	}
	raw := output.Bytes()
	if e != nil {
		return Assignment{}, fmt.Errorf("quota allocation: %w: %s", e, string(raw))
	}
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
