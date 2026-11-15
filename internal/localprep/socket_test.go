package localprep_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/client"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/localprep"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/service"
)

func TestSameStoreSocketCannotHandoffUnacknowledgedCooperativePreparation(t *testing.T) {
	s, _ := fixture(t)
	held, e := s.RepairLease("laptop", "local-writer")
	if e != nil {
		t.Fatal(e)
	}
	m := localprep.Manager{State: s.State, Data: s.Data}
	observed, e := m.Prepare(context.Background(), "laptop", held.Lease.Token, false, false)
	if e != nil {
		t.Fatal(e)
	}
	root, e := os.MkdirTemp("/tmp", "sb-local-sock-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	socket := filepath.Join(root, "api.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- service.Serve(ctx, service.Config{Store: s, Socket: socket, Operators: map[uint32]bool{uint32(os.Geteuid()): true}})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(10 * time.Second):
			t.Error("socket service did not stop")
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		if _, e = client.Call(context.Background(), socket, protocol.Request{Version: protocol.Version, Operation: "status", Key: "laptop"}); e == nil {
			ready = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		t.Fatal("socket did not become ready")
	}
	for _, request := range []protocol.Request{
		{Operation: "checkpoint", Key: "laptop", LeaseToken: held.Lease.Token},
		{Operation: "release", Key: "laptop", LeaseToken: held.Lease.Token},
		{Operation: "lease", Key: "laptop", Holder: "other-writer", RequestID: "other-lease"},
		{Operation: "setup", Key: "laptop", Holder: "other-writer", RequestID: "other-setup"},
		{Operation: "run", Key: "laptop", Holder: "other-writer", RequestID: "other-run", Arguments: []string{"/bin/sh", "-c", "exit 0"}},
	} {
		request.Version = protocol.Version
		if _, e = client.Call(context.Background(), socket, request); e == nil {
			t.Fatalf("socket bypassed pending journal for %s", request.Operation)
		}
	}
	if _, e = s.ArchiveWithGrace("laptop", 0); e == nil {
		t.Fatal("archive erased cooperative ownership")
	}
	current, e := s.Status("laptop")
	if e != nil || len(current) != 1 || current[0].Lease == nil || current[0].Lease.Holder != "local-writer" || current[0].Lease.Execution != nil {
		t.Fatal("failed operation changed local owner", e)
	}
	if _, e = m.Acknowledge(context.Background(), "laptop", held.Lease.Token, observed.Digest, true); e != nil {
		t.Fatal(e)
	}
	if _, e = client.Call(context.Background(), socket, protocol.Request{Version: protocol.Version, Operation: "checkpoint", Key: "laptop", LeaseToken: held.Lease.Token}); e != nil {
		t.Fatal(e)
	}
	if _, e = client.Call(context.Background(), socket, protocol.Request{Version: protocol.Version, Operation: "release", Key: "laptop", LeaseToken: held.Lease.Token}); e != nil {
		t.Fatal(e)
	}
}
