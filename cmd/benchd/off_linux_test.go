package main

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/activation"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/service"
)

// listening returns this process's TCP sockets in LISTEN state: entries of
// /proc/self/net/tcp{,6} whose inode is one of /proc/self/fd's sockets.
func listening(t *testing.T) []string {
	t.Helper()
	inodes := map[string]bool{}
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for _, fd := range fds {
		if target, err := os.Readlink(filepath.Join("/proc/self/fd", fd.Name())); err == nil && strings.HasPrefix(target, "socket:[") {
			inodes[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] = true
		}
	}
	var found []string
	for _, table := range []string{"/proc/self/net/tcp", "/proc/self/net/tcp6"} {
		f, err := os.Open(table)
		if err != nil {
			t.Fatal(err)
		}
		lines := bufio.NewScanner(f)
		for lines.Scan() {
			fields := strings.Fields(lines.Text())
			if len(fields) > 9 && fields[3] == "0A" && inodes[fields[9]] {
				found = append(found, table+" "+fields[1])
			}
		}
		f.Close()
	}
	return found
}

// With remote_access off, the running daemon holds no TCP listener, made no
// key directory and never built remote access.
func TestOffStateOpensNoPortAndNoKey(t *testing.T) {
	calls := countNew(t)
	// The check sees a listener of this process when there is one.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if len(listening(t)) != 1 {
		t.Fatal("the listener check missed this process's own listener", listening(t))
	}
	probe.Close()
	if found := listening(t); len(found) != 0 {
		t.Fatal("listeners before the start", found)
	}
	c := daemonConfig(t, activation.Switches{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(daemonArgs(c), func() (activation.Switches, error) { return activation.Switches{}, nil }, func(_ context.Context, config service.Config) error {
			if config.Remote != nil {
				t.Error("remote access built while off")
			}
			return service.Serve(ctx, config)
		})
	}()
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", c.Socket)
	}
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true, DialContext: dial}}
	answered := false
	for deadline := time.Now().Add(10 * time.Second); !answered && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if response, err := client.Post("http://local/v2/remote/status", "application/json", strings.NewReader("{}")); err == nil {
			answered = response.StatusCode == http.StatusNotFound
			response.Body.Close()
		}
	}
	found := listening(t)
	_, keyErr := os.Lstat(filepath.Join(c.Store.State, "remote"))
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !answered || len(found) != 0 || !os.IsNotExist(keyErr) || calls.Load() != 0 {
		t.Fatalf("off: remote route 404 %t, listeners %v, key directory %v, New called %d", answered, found, keyErr, calls.Load())
	}
}
