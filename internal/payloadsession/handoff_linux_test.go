//go:build linux

package payloadsession

import "testing"

func TestFixedTargetOnlyTheThreeArgvsAndTheBuiltEnvironment(t *testing.T) {
	env := []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/d/home", "TMPDIR=/d/tmp", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "LANG=C.UTF-8"}
	bash, sftp := "/usr/bin/bash", "/usr/local/libexec/bench-sftp"
	for name, c := range map[string]struct {
		h  Handoff
		ok bool
	}{
		"login shell":       {Handoff{Path: bash, Argv: []string{"bash", "-l"}, Env: env}, true},
		"exec":              {Handoff{Path: bash, Argv: []string{"bash", "-c", "make"}, Env: env}, true},
		"sftp":              {Handoff{Path: sftp, Argv: []string{"bench-sftp"}, Env: env}, true},
		"pty with term":     {Handoff{Path: bash, Argv: []string{"bash", "-l"}, Env: append(env[:len(env):len(env)], "TERM=xterm"), PTY: true}, true},
		"pty without term":  {Handoff{Path: bash, Argv: []string{"bash", "-l"}, Env: env, PTY: true}, false},
		"term without pty":  {Handoff{Path: bash, Argv: []string{"bash", "-l"}, Env: append(env[:len(env):len(env)], "TERM=xterm")}, false},
		"other argv":        {Handoff{Path: bash, Argv: []string{"bash", "-i"}, Env: env}, false},
		"sftp argv on bash": {Handoff{Path: bash, Argv: []string{"bench-sftp"}, Env: env}, false},
		"bash argv on sftp": {Handoff{Path: sftp, Argv: []string{"bash", "-l"}, Env: env}, false},
		"exec extra":        {Handoff{Path: bash, Argv: []string{"bash", "-c", "x", "y"}, Env: env}, false},
		"other path":        {Handoff{Path: "/bin/sh", Argv: []string{"bash", "-l"}, Env: env}, false},
		"loader variable":   {Handoff{Path: bash, Argv: []string{"bash", "-l"}, Env: append(env[:len(env):len(env)], "LD_PRELOAD=/x.so")}, false},
		"duplicate home":    {Handoff{Path: bash, Argv: []string{"bash", "-l"}, Env: append(env[:len(env):len(env)], "HOME=/x")}, false},
		"other path value":  {Handoff{Path: bash, Argv: []string{"bash", "-l"}, Env: append([]string{"PATH=/tmp"}, env[1:]...)}, false},
		"no equals":         {Handoff{Path: bash, Argv: []string{"bash", "-l"}, Env: append(env[:len(env):len(env)], "HOME")}, false},
	} {
		if fixedTarget(c.h) != c.ok {
			t.Errorf("%s: want %v", name, c.ok)
		}
	}
}
