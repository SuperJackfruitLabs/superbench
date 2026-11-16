//go:build linux

package payloadexec

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExistingLayoutLoadBindsRegisteredIdentityWithoutCreatingCheckout(t *testing.T) {
	p, l, r, path, raw := savedLayout(t)
	got, err := LoadLayout(p, r)
	if err != nil || got.ID != l.ID || got.Key != l.Key {
		t.Fatalf("existing registration: %#v %v", got, err)
	}
	for _, change := range []func(*Request){
		func(r *Request) { r.LayoutID = strings.Repeat("f", 32) },
		func(r *Request) { r.Member = "unregistered" },
		func(r *Request) { r.Key = "absent" },
		func(r *Request) { r.PolicyDigest = strings.Repeat("f", 64) },
		func(r *Request) { r.Member = "" },
	} {
		candidate := r
		change(&candidate)
		if _, err := LoadLayout(p, candidate); !errors.Is(err, ErrPolicy) {
			t.Fatalf("foreign registration accepted: %v", err)
		}
	}
	rootRequest := r
	rootRequest.Root, rootRequest.Member = true, ""
	if _, err := LoadLayout(p, rootRequest); err != nil {
		t.Fatalf("explicit aggregate root selection: %v", err)
	}
	if _, err := os.Lstat(p.DataRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspection created allocation root: %v", err)
	}
	gotRaw, err := os.ReadFile(path)
	if err != nil || string(gotRaw) != string(raw) {
		t.Fatalf("inspection changed registration: %q %v", gotRaw, err)
	}
}

func TestLayoutRegistrationCannotUseSymlinkHardlinkOrExposedAuthority(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "group-readable", "unknown-field"} {
		t.Run(kind, func(t *testing.T) {
			p, _, r, path, raw := savedLayout(t)
			other := filepath.Join(t.TempDir(), "preserved")
			if err := os.WriteFile(other, raw, 0600); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "symlink", "hardlink":
				if err = os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if kind == "symlink" {
					err = os.Symlink(other, path)
				} else {
					err = os.Link(other, path)
				}
			case "group-readable":
				err = os.Chmod(path, 0640)
			case "unknown-field":
				err = os.WriteFile(path, append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"unreviewed":true}`)...), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := LoadLayout(p, r); !errors.Is(err, ErrPolicy) {
				t.Fatalf("unsafe registration accepted: %v", err)
			}
			got, err := os.ReadFile(other)
			if err != nil || string(got) != string(raw) {
				t.Fatalf("refusal changed unrelated authority: %q %v", got, err)
			}
		})
	}
}

func savedLayout(t *testing.T) (Policy, Layout, Request, string, []byte) {
	t.Helper()
	p, l := layoutFixture(t, false)
	p.DaemonUID, p.DaemonGID = uint32(os.Getuid()), uint32(os.Getgid())
	p.PayloadUID, p.PayloadGID = p.DaemonUID+1, p.DaemonGID+1
	root := t.TempDir()
	p.DataRoot, p.StateRoot = filepath.Join(root, "data"), filepath.Join(root, "state")
	var err error
	l.PolicyDigest, err = policyDigest(p)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := l.Bytes(p)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(p.StateRoot, "payload-layouts", l.Key+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	r := launchRequest()
	r.PolicyDigest = l.PolicyDigest
	return p, l, r, path, raw
}
