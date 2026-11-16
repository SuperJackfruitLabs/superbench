//go:build linux

package payloadlaunch

import (
	"os"
	"os/exec"
	"reflect"
)

// CommandGuard pins the fixed helper invocation and its prepared pipe endpoints.
// It is transport integrity only; the caller must separately revalidate installed
// authority and consume Check inside its actual execution admission gate.
type CommandGuard struct {
	command               *exec.Cmd
	environment           []string
	stdin, stdout, stderr *os.File
}

func GuardCommand(cmd *exec.Cmd) CommandGuard {
	if cmd == nil {
		return CommandGuard{}
	}
	in, _ := cmd.Stdin.(*os.File)
	out, _ := cmd.Stdout.(*os.File)
	errOut, _ := cmd.Stderr.(*os.File)
	return CommandGuard{command: cmd, environment: append([]string(nil), cmd.Env...), stdin: in, stdout: out, stderr: errOut}
}

func (g CommandGuard) Check() error {
	c := g.command
	if c == nil || c.Process != nil || c.ProcessState != nil || c.Path != "/usr/bin/sudo" || !reflect.DeepEqual(c.Args, []string{"sudo", "-n", "--", Helper}) || c.Dir != "/" || len(c.ExtraFiles) != 0 || c.SysProcAttr != nil || !reflect.DeepEqual(c.Env, g.environment) || g.stdin == nil || g.stdout == nil || g.stderr == nil || c.Stdin != g.stdin || c.Stdout != g.stdout || c.Stderr != g.stderr {
		return ErrTransport
	}
	return nil
}
