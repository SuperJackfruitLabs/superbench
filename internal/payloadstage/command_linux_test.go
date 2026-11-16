//go:build linux

package payloadstage

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadlaunch"
)

func commandTransport(t *testing.T) (*transport, *exec.Cmd, *int) {
	t.Helper()
	cmd := exec.Command("payload", "argument")
	cmd.Env = []string{"LANG=C"}
	scope := &execution.Execution{ID: strings.Repeat("1", 32), Root: t.TempDir(), HostID: strings.Repeat("2", 64), BootID: "00000000-0000-0000-0000-000000000001", Phase: "starting"}
	bridge, err := payloadlaunch.Prepare(cmd, payloadlaunch.Target{Key: "fixture", LayoutID: strings.Repeat("3", 32), Member: "a", PolicyDigest: strings.Repeat("4", 64), Kind: "payload"}, scope, payloadlaunch.GitInput{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close() })
	checks := new(int)
	return preparedTransport(bridge, cmd, func() error { *checks++; return nil }), cmd, checks
}

func TestPreparedCommandCheckRetainsEnvironmentAndStdio(t *testing.T) {
	transport, _, checks := commandTransport(t)
	if err := transport.Check(); err != nil || *checks != 1 {
		t.Fatalf("unchanged command refused: %v checks=%d", err, *checks)
	}
}

func TestPreparedCommandCheckRefusesMutationBeforeAuthority(t *testing.T) {
	for name, mutate := range map[string]func(*exec.Cmd){
		"environment replacement": func(c *exec.Cmd) { c.Env = []string{"PATH=/untrusted"} },
		"environment in place":    func(c *exec.Cmd) { c.Env[0] = "PATH=/untrusted" },
		"environment append":      func(c *exec.Cmd) { c.Env = append(c.Env, "LD_PRELOAD=untrusted") },
		"stdin":                   func(c *exec.Cmd) { c.Stdin = bytes.NewReader([]byte("untrusted")) },
		"stdout":                  func(c *exec.Cmd) { c.Stdout = io.Discard },
		"stderr":                  func(c *exec.Cmd) { c.Stderr = io.Discard },
		"path":                    func(c *exec.Cmd) { c.Path = "/bin/sh" },
		"arguments":               func(c *exec.Cmd) { c.Args[0] = "sh" },
		"directory":               func(c *exec.Cmd) { c.Dir = "/untrusted" },
		"extra descriptor":        func(c *exec.Cmd) { c.ExtraFiles = []*os.File{c.Stdin.(*os.File)} },
		"process attributes":      func(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{} },
	} {
		t.Run(name, func(t *testing.T) {
			transport, cmd, checks := commandTransport(t)
			mutate(cmd)
			if err := transport.Check(); !errors.Is(err, ErrRegistration) || *checks != 0 {
				t.Fatalf("changed command reached authority/launch: %v checks=%d", err, *checks)
			}
		})
	}
}
