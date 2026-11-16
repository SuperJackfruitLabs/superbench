//go:build linux && serviceidentityfixture

package payloadexec

// This opt-in test exercises the read-only action through real Exec in a root
// child, daemon-owned pipes and payload-owned 0700 nodes. It is not a sudo,
// cgroup-fencing, service-owner or public-route conformance claim.
import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/serviceidentity"
)

func TestInspectionExecChild(t *testing.T) {
	if os.Getenv("SUPERBENCH_INSPECTION_CHILD") != "1" {
		return
	}
	if err := Exec(); err != nil {
		code := "internal"
		switch {
		case errors.Is(err, ErrRequest):
			code = "request"
		case errors.Is(err, ErrPolicy):
			code = "policy"
		case errors.Is(err, ErrBoundary):
			code = "boundary"
		}
		fmt.Fprintf(os.Stderr, "INSPECTION_REFUSAL %s %s\n", RefusalCode(err), code)
		os.Exit(17)
	}
	os.Exit(0)
}

func TestInstalledReadOnlyRegistrationInspectionFixture(t *testing.T) {
	helperFixtureNamespaceGuard(t) // No effects before concrete namespace proof.
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	base, err := os.MkdirTemp("/var/tmp", "superbench-inspection-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(base, 0755); err != nil {
		t.Fatal(err)
	}
	etc := filepath.Join(base, "etc")
	if err = os.MkdirAll(filepath.Join(etc, "superbench"), 0755); err != nil {
		t.Fatal(err)
	}
	if err = syscall.Mount(etc, "/etc", "", syscall.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	var created []string
	// Cleanup is exact, nonrecursive and only after all children have terminated.
	defer func() {
		if err := syscall.Unmount("/etc", 0); err != nil {
			t.Error(err)
			return
		}
		if t.Failed() {
			t.Logf("preserve failed inspection fixture %s", base)
			return
		}
		for i := len(created) - 1; i >= 0; i-- {
			if err := os.Remove(created[i]); err != nil {
				t.Error(err)
			}
		}
		for _, path := range []string{filepath.Join(etc, "superbench"), etc, base} {
			if err := os.Remove(path); err != nil {
				t.Error(err)
			}
		}
	}()
	mkdir := func(path string, uid, gid int) {
		t.Helper()
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		created = append(created, path)
		if err := os.Chown(path, uid, gid); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path string, raw []byte, uid, gid int, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, raw, mode); err != nil {
			t.Fatal(err)
		}
		retainedPath := path
		if strings.HasPrefix(path, "/etc/") {
			retainedPath = filepath.Join(etc, strings.TrimPrefix(path, "/etc/"))
		}
		created = append(created, retainedPath)
		if err := os.Chown(path, uid, gid); err != nil {
			t.Fatal(err)
		}
	}
	p := Policy{DaemonUID: 61001, DaemonGID: 61003, PayloadUID: 61002, PayloadGID: 61004, DataRoot: filepath.Join(base, "data"), StateRoot: filepath.Join(base, "state"), CgroupRoot: filepath.Join(base, "never-create-cgroup")}
	mkdir(p.DataRoot, int(p.DaemonUID), int(p.DaemonGID))
	mkdir(p.StateRoot, int(p.DaemonUID), int(p.DaemonGID))
	benches := filepath.Join(p.DataRoot, "benches")
	mkdir(benches, int(p.DaemonUID), int(p.DaemonGID))
	root := filepath.Join(benches, "example")
	mkdir(root, int(p.PayloadUID), int(p.PayloadGID))
	for _, name := range []string{".superbench-git", ".superbench-runtime", ".superbench-runtime/.root", ".superbench-runtime/.root/home", ".superbench-runtime/.root/tmp", "api", "web", ".superbench-git/api", ".superbench-git/web", ".superbench-runtime/api", ".superbench-runtime/api/home", ".superbench-runtime/api/tmp", ".superbench-runtime/web", ".superbench-runtime/web/home", ".superbench-runtime/web/tmp"} {
		mkdir(filepath.Join(root, name), int(p.PayloadUID), int(p.PayloadGID))
	}
	node := func(name string) Node {
		t.Helper()
		info, e := os.Stat(filepath.Join(root, name))
		if e != nil {
			t.Fatal(e)
		}
		st := info.Sys().(*syscall.Stat_t)
		return Node{uint64(st.Dev), st.Ino}
	}
	sha, err := policyDigest(p)
	if err != nil {
		t.Fatal(err)
	}
	l := Layout{Version: LayoutVersion, Key: "example", ID: strings.Repeat("a", 32), PolicyDigest: sha, Root: node("."), RootHome: node(".superbench-runtime/.root/home"), RootTemp: node(".superbench-runtime/.root/tmp")}
	for _, name := range []string{"api", "web"} {
		l.Members = append(l.Members, LayoutMember{name, node(name), node(".superbench-git/" + name), node(".superbench-runtime/" + name + "/home"), node(".superbench-runtime/" + name + "/tmp")})
	}
	raw, err := l.Bytes(p)
	if err != nil {
		t.Fatal(err)
	}
	metadata := filepath.Join(p.StateRoot, "payload-layouts")
	mkdir(metadata, int(p.DaemonUID), int(p.DaemonGID))
	registration := filepath.Join(metadata, l.Key+".json")
	write(registration, raw, int(p.DaemonUID), int(p.DaemonGID), 0600)
	policyRaw, err := p.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	write(FixedPolicy, policyRaw, 0, 0, 0600)
	registry, err := serviceidentity.ForPolicy(p)
	if err != nil {
		t.Fatal(err)
	}
	registryRaw, err := registry.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	write(serviceidentity.FixedRegistry, registryRaw, 0, 0, 0644)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	request := InspectionRequest{InspectionVersion, l.Key, l.ID, l.PolicyDigest}
	run := func(t *testing.T, r InspectionRequest, pipeUID int) ([]byte, string, error) {
		t.Helper()
		body, e := r.Bytes()
		if e != nil {
			t.Fatal(e)
		}
		framed := make([]byte, 4)
		binary.BigEndian.PutUint32(framed, uint32(len(body)))
		framed = append(framed, body...)
		inR, inW, e := os.Pipe()
		if e != nil {
			t.Fatal(e)
		}
		defer inR.Close()
		defer inW.Close()
		outR, outW, e := os.Pipe()
		if e != nil {
			t.Fatal(e)
		}
		defer outR.Close()
		defer outW.Close()
		errR, errW, e := os.Pipe()
		if e != nil {
			t.Fatal(e)
		}
		defer errR.Close()
		defer errW.Close()
		for _, f := range []*os.File{inR, inW, outR, outW, errR, errW} {
			if e = f.Chown(pipeUID, int(p.DaemonGID)); e != nil {
				t.Fatal(e)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestInspectionExecChild$")
		cmd.Env = []string{"SUPERBENCH_INSPECTION_CHILD=1"}
		cmd.Stdin = inR
		cmd.Stdout = outW
		cmd.Stderr = errW
		if e = cmd.Start(); e != nil {
			t.Fatal(e)
		}
		waited := false
		defer func() {
			cancel()
			if !waited {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		}()
		_ = inR.Close()
		_ = outW.Close()
		_ = errW.Close()
		writeDone := make(chan error, 1)
		go func() { _, e := inW.Write(framed); _ = inW.Close(); writeDone <- e }()
		output, e := io.ReadAll(io.LimitReader(outR, MaxLayout+5))
		if e != nil {
			t.Fatal(e)
		}
		diagnostic, e := io.ReadAll(io.LimitReader(errR, 1025))
		if e != nil || len(diagnostic) > 128 {
			t.Fatalf("unbounded diagnostic %d %v", len(diagnostic), e)
		}
		result := cmd.Wait()
		waited = true
		_ = <-writeDone
		if ctx.Err() != nil {
			t.Fatalf("inspection child exceeded bound: %v", ctx.Err())
		}
		return output, string(diagnostic), result
	}
	positive := func(t *testing.T) {
		t.Helper()
		output, diagnostic, e := run(t, request, int(p.DaemonUID))
		if e != nil || diagnostic != "" || len(output) < 4 || int(binary.BigEndian.Uint32(output)) != len(output)-4 {
			t.Fatalf("inspection frame: %d %v", len(output), e)
		}
		got, e := ParseLayout(p, output[4:])
		if e != nil || !reflect.DeepEqual(got, l) {
			t.Fatalf("whole physical vector mismatch: %#v %v", got, e)
		}
	}
	positive(t)
	t.Log("INSPECTION_ACTUAL_ROOT_FULL_VECTOR")
	// Every refusal starts with a successful identical actual Exec inspection.
	for _, scenario := range []string{"foreign-BID", "foreign-policy", "wrong-pipe-owner", "later-member-replaced", "later-member-symlink", "later-member-mode"} {
		t.Run(scenario, func(t *testing.T) {
			positive(t)
			candidate := request
			pipeUID := int(p.DaemonUID)
			path := filepath.Join(root, ".superbench-runtime/web/tmp")
			backup := filepath.Join(root, "retained-original-tmp")
			switch scenario {
			case "foreign-BID":
				candidate.LayoutID = strings.Repeat("f", 32)
			case "foreign-policy":
				candidate.PolicyDigest = strings.Repeat("f", 64)
			case "wrong-pipe-owner":
				pipeUID = int(p.PayloadUID)
			case "later-member-replaced", "later-member-symlink":
				if err := os.Rename(path, backup); err != nil {
					t.Fatal(err)
				}
				if scenario == "later-member-replaced" {
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.Chown(path, int(p.PayloadUID), int(p.PayloadGID)); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Symlink(backup, path); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := os.Remove(path); err != nil {
						t.Error(err)
					}
					if err := os.Rename(backup, path); err != nil {
						t.Error(err)
					}
				}()
			case "later-member-mode":
				if err := os.Chmod(path, 0777); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := os.Chmod(path, 0700); err != nil {
						t.Error(err)
					}
				}()
			}
			output, diagnostic, e := run(t, candidate, pipeUID)
			var exit *exec.ExitError
			expected := "INSPECTION_REFUSAL inspection boundary\n"
			if scenario == "foreign-BID" || scenario == "foreign-policy" {
				expected = "INSPECTION_REFUSAL inspection policy\n"
			}
			if scenario == "wrong-pipe-owner" {
				expected = "INSPECTION_REFUSAL stdio boundary\n"
			}
			if diagnostic != expected || len(output) != 0 || !errors.As(e, &exit) || exit.ExitCode() != 17 {
				t.Fatalf("unsafe inspection published result: %d %v", len(output), e)
			}
			current, e := os.ReadFile(registration)
			if e != nil || !bytes.Equal(current, raw) {
				t.Fatalf("inspection changed registration: %v", e)
			}
			if _, e = os.Lstat(p.CgroupRoot); !errors.Is(e, os.ErrNotExist) {
				t.Fatalf("inspection touched cgroups: %v", e)
			}
		})
	}

	for _, scenario := range []string{"registration-replaced-after-open", "data-parent-replaced-after-open"} {
		t.Run(scenario, func(t *testing.T) {
			positive(t)
			held, e := inspectNodes(p, l)
			if e != nil {
				t.Fatal(e)
			}
			defer held.close()
			if e = held.check(p, l); e != nil {
				t.Fatalf("valid held baseline: %v", e)
			}
			if scenario == "registration-replaced-after-open" {
				backup := filepath.Join(p.StateRoot, "preserved-registration")
				if e = os.Rename(registration, backup); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(registration, raw, 0600); e != nil {
					t.Fatal(e)
				}
				if e = os.Chown(registration, int(p.DaemonUID), int(p.DaemonGID)); e != nil {
					t.Fatal(e)
				}
				defer func() {
					if e := os.Remove(registration); e != nil {
						t.Error(e)
					}
					if e := os.Rename(backup, registration); e != nil {
						t.Error(e)
					}
				}()
			} else {
				backup := filepath.Join(base, "retained-data-parent")
				if e = os.Rename(p.DataRoot, backup); e != nil {
					t.Fatal(e)
				}
				if e = os.Mkdir(p.DataRoot, 0700); e != nil {
					t.Fatal(e)
				}
				if e = os.Chown(p.DataRoot, int(p.DaemonUID), int(p.DaemonGID)); e != nil {
					t.Fatal(e)
				}
				if e = os.Rename(filepath.Join(backup, "benches"), benches); e != nil {
					t.Fatal(e)
				}
				defer func() {
					if e := os.Rename(benches, filepath.Join(backup, "benches")); e != nil {
						t.Error(e)
					}
					if e := os.Remove(p.DataRoot); e != nil {
						t.Error(e)
					}
					if e := os.Rename(backup, p.DataRoot); e != nil {
						t.Error(e)
					}
				}()
			}
			if e = held.check(p, l); !errors.Is(e, ErrBoundary) {
				t.Fatalf("changed retained authority accepted: %v", e)
			}
		})
	}
	positive(t)
	t.Log("INSPECTION_SCOPE_FREE_REFUSALS_NO_CGROUP_OR_EXEC")
}
