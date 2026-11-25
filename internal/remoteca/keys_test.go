package remoteca

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func openTemp(t *testing.T) (*Authority, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "remote")
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return a, dir
}

func TestOpenCreatesKeysOnceWithPrivateModes(t *testing.T) {
	a, dir := openTemp(t)
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() || st.Mode().Perm() != 0o700 {
		t.Fatalf("dir %v %v", st.Mode(), err)
	}
	for _, name := range []string{caFile, hostFile} {
		st, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 {
			t.Fatalf("%s %v %v", name, st.Mode(), err)
		}
	}
	if !strings.HasPrefix(a.HostKeyLine(), "ssh-ed25519 AAAA") || strings.ContainsAny(a.HostKeyLine(), "\n") {
		t.Fatalf("host key line %q", a.HostKeyLine())
	}
	if !strings.HasPrefix(a.HostFingerprint(), "SHA256:") {
		t.Fatalf("fingerprint %q", a.HostFingerprint())
	}
	if a.HostSigner().PublicKey().Type() != ssh.KeyAlgoED25519 {
		t.Fatal("host key is not ed25519")
	}
	if string(a.ca.PublicKey().Marshal()) == string(a.host.PublicKey().Marshal()) {
		t.Fatal("CA and host key are the same key")
	}
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if b.HostFingerprint() != a.HostFingerprint() || string(b.ca.PublicKey().Marshal()) != string(a.ca.PublicKey().Marshal()) {
		t.Fatal("second Open did not load the same keys")
	}
}

func TestOpenRefusesUnsafeKeyFiles(t *testing.T) {
	foreignKey := func(t *testing.T) []byte {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		block, err := ssh.MarshalPrivateKey(k, "")
		if err != nil {
			t.Fatal(err)
		}
		return pem.EncodeToMemory(block)
	}
	cases := map[string]func(t *testing.T, dir string){
		"symlink": func(t *testing.T, dir string) {
			p := filepath.Join(dir, caFile)
			target := filepath.Join(t.TempDir(), "elsewhere")
			mustRename(t, p, target)
			if err := os.Symlink(target, p); err != nil {
				t.Fatal(err)
			}
		},
		"mode 0644": func(t *testing.T, dir string) {
			if err := os.Chmod(filepath.Join(dir, hostFile), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"hardlink": func(t *testing.T, dir string) {
			if err := os.Link(filepath.Join(dir, caFile), filepath.Join(dir, "copy")); err != nil {
				t.Fatal(err)
			}
		},
		"directory": func(t *testing.T, dir string) {
			p := filepath.Join(dir, hostFile)
			mustRemove(t, p)
			if err := os.Mkdir(p, 0o700); err != nil {
				t.Fatal(err)
			}
		},
		"garbage": func(t *testing.T, dir string) {
			writeKey(t, filepath.Join(dir, caFile), []byte("not a key"))
		},
		"empty": func(t *testing.T, dir string) {
			writeKey(t, filepath.Join(dir, caFile), nil)
		},
		"not ed25519": func(t *testing.T, dir string) {
			writeKey(t, filepath.Join(dir, hostFile), foreignKey(t))
		},
		"oversized": func(t *testing.T, dir string) {
			writeKey(t, filepath.Join(dir, caFile), make([]byte, maxKeyFile+1))
		},
		"dir mode 0755": func(t *testing.T, dir string) {
			if err := os.Chmod(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"dir symlink": func(t *testing.T, dir string) {
			target := filepath.Join(t.TempDir(), "real")
			mustRename(t, dir, target)
			if err := os.Symlink(target, dir); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			_, dir := openTemp(t)
			spoil(t, dir)
			if _, err := Open(dir); !errors.Is(err, ErrKey) {
				t.Fatalf("Open = %v, want ErrKey", err)
			}
		})
	}
}

func TestOpenDoesNotCreateParents(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "missing", "remote")); err == nil {
		t.Fatal("Open created a missing parent")
	}
}

func TestOpenCompletesAHalfCreatedDirectory(t *testing.T) {
	_, dir := openTemp(t)
	mustRemove(t, filepath.Join(dir, hostFile))
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, hostFile)); err != nil || a.HostSigner() == nil {
		t.Fatalf("host key not recreated: %v", err)
	}
}

func TestFingerprint(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sp, _ := ssh.NewPublicKey(pub)
	got, err := Fingerprint(string(ssh.MarshalAuthorizedKey(sp)))
	if err != nil || got != ssh.FingerprintSHA256(sp) {
		t.Fatalf("Fingerprint = %q %v", got, err)
	}
	if _, err := Fingerprint("garbage"); !errors.Is(err, ErrKey) {
		t.Fatalf("garbage: %v", err)
	}
}

func writeKey(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustRename(t *testing.T, from, to string) {
	t.Helper()
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
}

func mustRemove(t *testing.T, p string) {
	t.Helper()
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
}
