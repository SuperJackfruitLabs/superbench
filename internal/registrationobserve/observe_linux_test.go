//go:build linux

package registrationobserve

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/serviceidentity"
)

func observedFixture(t *testing.T) (payloadpolicy.Policy, Request, model.Bench, payloadexec.Layout) {
	t.Helper()
	p := payloadpolicy.Policy{DaemonUID: 1001, DaemonGID: 1001, PayloadUID: 1002, PayloadGID: 1002, DataRoot: "/data", StateRoot: "/state", CgroupRoot: "/scope"}
	registry, err := serviceidentity.ForPolicy(p)
	if err != nil {
		t.Fatal(err)
	}
	r := Request{Key: "card", BenchID: strings.Repeat("a", 32), Holder: "owner", LeaseToken: "private-fixture"}
	l := payloadexec.Layout{Version: payloadpolicy.LayoutVersion, Key: r.Key, ID: r.BenchID, PolicyDigest: registry.PolicySHA256, Root: payloadexec.Node{Device: 1, Inode: 1}, RootHome: payloadexec.Node{Device: 1, Inode: 2}, RootTemp: payloadexec.Node{Device: 1, Inode: 3}, Members: []payloadexec.LayoutMember{{Name: "app", Checkout: payloadexec.Node{Device: 1, Inode: 4}, Admin: payloadexec.Node{Device: 1, Inode: 5}, Home: payloadexec.Node{Device: 1, Inode: 6}, Temp: payloadexec.Node{Device: 1, Inode: 7}}}}
	b := model.Bench{Key: r.Key, Path: "/data/benches/card", Lease: &model.Lease{Holder: r.Holder}, Workspace: &model.Workspace{ID: r.BenchID, Phase: "ready", Members: []model.WorkspaceMember{{Name: "app", Path: "/data/benches/card/app"}}, Checkpoint: &model.WorkspaceCheckpoint{GenerationID: strings.Repeat("b", 32), Digest: strings.Repeat("c", 64), Seal: strings.Repeat("d", 40), Members: []model.WorkspaceCommit{{Name: "app", Commit: strings.Repeat("e", 40)}}}}}
	if err = l.Validate(p); err != nil {
		t.Fatal(err)
	}
	if err = ready(p, r, b); err != nil {
		t.Fatal(err)
	}
	if err = joinLayout(p, r, b, l); err != nil {
		t.Fatal(err)
	}
	return p, r, b, l
}

func TestObservationRequiresActualInstalledAuthorityBeforeStateEffects(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "absent-state")
	r := Request{Key: "card", BenchID: strings.Repeat("a", 32), Holder: "owner", LeaseToken: "private-fixture"}
	got, err := Observe(context.Background(), bench.Store{State: missing}, serviceidentity.Authority{}, r)
	if !errors.Is(err, ErrObservation) || got != nil {
		t.Fatalf("uninstalled authority adopted: %#v %v", got, err)
	}
	if _, err = os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state created before authority: %v", err)
	}
}

func TestObservationJoinsWholeDurableVectorAndRefusesUnsettledStates(t *testing.T) {
	for _, kind := range []string{"wrong-bid", "wrong-holder", "pending", "stage", "archive", "transfer", "repair", "stop-requested", "running", "wrong-root", "wrong-member-path", "wrong-cp-member", "wrong-cp-commit", "wrong-generation", "wrong-digest", "scalar-layout", "missing-layout-member", "wrong-layout-id"} {
		t.Run(kind, func(t *testing.T) {
			p, r, b, l := observedFixture(t)
			switch kind {
			case "wrong-bid":
				b.Workspace.ID = strings.Repeat("f", 32)
			case "wrong-holder":
				b.Lease.Holder = "later-owner"
			case "pending":
				b.Workspace.Pending = &model.WorkspaceCheckpoint{}
			case "stage":
				b.Workspace.Stage = &model.WorkspaceStage{}
			case "archive":
				b.Workspace.Archive = &model.WorkspaceArchive{}
			case "transfer":
				b.Workspace.Transfer = &model.WorkspaceTransfer{}
			case "repair":
				b.Workspace.InitializationRepair = &model.WorkspaceInitializationRepair{}
			case "stop-requested":
				b.Lease.StopRequested = true
			case "running":
				b.Lease.Execution = &model.Execution{Phase: "running", Root: p.CgroupRoot}
			case "wrong-root":
				b.Path = "/different"
			case "wrong-member-path":
				b.Workspace.Members[0].Path = "/different"
			case "wrong-cp-member":
				b.Workspace.Checkpoint.Members[0].Name = "other"
			case "wrong-cp-commit":
				b.Workspace.Checkpoint.Members[0].Commit = strings.Repeat("A", 40)
			case "wrong-generation":
				b.Workspace.Checkpoint.GenerationID = "named-generation"
			case "wrong-digest":
				b.Workspace.Checkpoint.Digest = strings.Repeat("A", 64)
			case "scalar-layout":
				l.Scalar = true
			case "missing-layout-member":
				l.Members = nil
			case "wrong-layout-id":
				l.ID = strings.Repeat("f", 32)
			}
			err := ready(p, r, b)
			if err == nil {
				err = joinLayout(p, r, b, l)
			}
			if !errors.Is(err, ErrObservation) {
				t.Fatalf("mismatched registration accepted: %v", err)
			}
		})
	}
}

func TestInspectionTransportIsFixedBoundedAndExactFramedResult(t *testing.T) {
	p, r, _, l := observedFixture(t)
	request := payloadexec.InspectionRequest{Version: payloadexec.InspectionVersion, Key: r.Key, LayoutID: r.BenchID, PolicyDigest: l.PolicyDigest}
	raw, err := request.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	cmd, out := inspectionCommand(context.Background(), raw)
	if cmd.Path != "/usr/bin/sudo" || !reflect.DeepEqual(cmd.Args, []string{"/usr/bin/sudo", "-n", "--", helper}) || cmd.Dir != "/" || !reflect.DeepEqual(cmd.Env, []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}) || len(cmd.ExtraFiles) != 0 || cmd.SysProcAttr != nil {
		t.Fatalf("inspection transport expanded: %#v", cmd)
	}
	if _, err = out.Write(bytes.Repeat([]byte("x"), payloadexec.MaxLayout+5)); !errors.Is(err, ErrObservation) || out.Len() != 0 {
		t.Fatalf("unbounded helper stdout: %d %v", out.Len(), err)
	}
	layout, err := l.Bytes(p)
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 4+len(layout))
	binary.BigEndian.PutUint32(frame, uint32(len(layout)))
	copy(frame[4:], layout)
	if got, err := inspectionResult(p, request, frame); err != nil || !reflect.DeepEqual(got, l) {
		t.Fatalf("valid exact observation: %#v %v", got, err)
	}
	foreign := request
	foreign.LayoutID = strings.Repeat("f", 32)
	for _, candidate := range [][]byte{frame[:3], frame[:len(frame)-1], append(bytes.Clone(frame), 'x'), bytes.Repeat([]byte("x"), payloadexec.MaxLayout+5)} {
		if _, err = inspectionResult(p, request, candidate); !errors.Is(err, ErrObservation) {
			t.Fatalf("inexact response accepted: %v", err)
		}
	}
	if _, err = inspectionResult(p, foreign, frame); !errors.Is(err, ErrObservation) {
		t.Fatalf("foreign response accepted: %v", err)
	}
}
