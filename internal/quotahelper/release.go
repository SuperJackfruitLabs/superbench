package quotahelper

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
)

// Release preserves the fixed logical retirement transport. The engine still
// validates the returned assignment against its retained original ownership.
func Release(c Config, key string, project uint32) (Assignment, error) {
	return release(c, key, project, os.Geteuid())
}
func release(c Config, key string, project uint32, uid int) (Assignment, error) {
	args := []string{}
	if c.PolicyOverride != "" {
		args = append(args, "--policy", c.PolicyOverride)
	}
	args = append(args, "release", "bench", key, fmt.Sprint(project))
	command := c.Command
	if uid != 0 {
		args = append([]string{"-n", command}, args...)
		command = "/usr/bin/sudo"
	}
	raw, err := exec.Command(command, args...).CombinedOutput()
	if err != nil {
		return Assignment{}, fmt.Errorf("quota release: %w: %s", err, raw)
	}
	var a Assignment
	if err = json.Unmarshal(raw, &a); err != nil {
		return a, err
	}
	return a, nil
}
