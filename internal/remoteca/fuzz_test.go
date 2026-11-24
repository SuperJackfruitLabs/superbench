package remoteca

import (
	"bytes"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

// FuzzVerify feeds arbitrary wire bytes to Verify. Nothing may be accepted
// except a certificate this authority issued, byte for byte.
func FuzzVerify(f *testing.F) {
	a, err := Open(filepath.Join(f.TempDir(), "remote"))
	if err != nil {
		f.Fatal(err)
	}
	other, err := Open(filepath.Join(f.TempDir(), "other"))
	if err != nil {
		f.Fatal(err)
	}
	pub, line := clientKey(f)
	issued, err := a.IssueWriter(line, testClaims(), testNow)
	if err != nil {
		f.Fatal(err)
	}
	foreign, err := other.IssueWriter(line, testClaims(), testNow)
	if err != nil {
		f.Fatal(err)
	}
	mine := parseLine(f, issued.Line).Marshal()
	f.Add(mine)
	f.Add(parseLine(f, foreign.Line).Marshal())
	f.Add(pub.Marshal())
	f.Add(a.ca.PublicKey().Marshal())
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		key, err := ssh.ParsePublicKey(data)
		if err != nil {
			return
		}
		user := "b-" + testBench
		if c, ok := key.(*ssh.Certificate); ok && len(c.ValidPrincipals) == 1 {
			user = c.ValidPrincipals[0]
		}
		claims, cert, err := a.Verify(user, key, testNow)
		if err != nil {
			if cert != nil || claims != (Claims{}) {
				t.Fatal("refusal returned a certificate or claims")
			}
			return
		}
		if !bytes.Equal(cert.Marshal(), mine) || claims != testClaims() {
			t.Fatalf("accepted a certificate this authority did not issue: %x", data)
		}
	})
}
