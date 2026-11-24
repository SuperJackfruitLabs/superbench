package remoteca

import (
	"crypto/dsa"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	testBench = "0123456789abcdef0123456789abcdef"
	testLease = "fedcba9876543210fedcba9876543210"
	testExec  = "00112233445566778899aabbccddeeff"
	testToken = "aaaabbbbccccddddeeeeffff00001111"
)

var testNow = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

func testClaims() Claims {
	return Claims{BenchID: testBench, LeaseID: testLease, Generation: 7, ExecutionID: testExec}
}

func clientKey(t testing.TB) (ssh.PublicKey, string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return sp, string(ssh.MarshalAuthorizedKey(sp))
}

func parseLine(t testing.TB, line string) *ssh.Certificate {
	t.Helper()
	k, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil || len(rest) != 0 {
		t.Fatalf("parse issued line: %v", err)
	}
	c, ok := k.(*ssh.Certificate)
	if !ok {
		t.Fatal("issued line is not a certificate")
	}
	return c
}

func TestPrincipal(t *testing.T) {
	if Principal(testBench) != "b-"+testBench {
		t.Fatal(Principal(testBench))
	}
}

func TestIssueWriterShape(t *testing.T) {
	a, _ := openTemp(t)
	pub, line := clientKey(t)
	issued, err := a.IssueWriter(line, testClaims(), testNow)
	if err != nil {
		t.Fatal(err)
	}
	cert := parseLine(t, issued.Line)
	if cert.CertType != ssh.UserCert || cert.KeyId != testLease {
		t.Fatalf("type %d key id %q", cert.CertType, cert.KeyId)
	}
	if len(cert.ValidPrincipals) != 1 || cert.ValidPrincipals[0] != "b-"+testBench {
		t.Fatalf("principals %v", cert.ValidPrincipals)
	}
	if cert.ValidBefore-cert.ValidAfter != uint64((30*time.Minute + time.Minute).Seconds()) {
		t.Fatalf("window %d", cert.ValidBefore-cert.ValidAfter)
	}
	if cert.ValidAfter != uint64(testNow.Add(-ClockSkew).Unix()) || cert.ValidBefore != uint64(testNow.Add(WriterValidity).Unix()) {
		t.Fatal("window not anchored at now")
	}
	if issued.ValidAfter.Unix() != int64(cert.ValidAfter) || issued.ValidBefore.Unix() != int64(cert.ValidBefore) {
		t.Fatal("Issued times disagree with the certificate")
	}
	if len(cert.CriticalOptions) != 0 {
		t.Fatalf("critical options %v", cert.CriticalOptions)
	}
	if len(cert.Extensions) != 2 || cert.Extensions["permit-pty"] != "" {
		t.Fatalf("extensions %v", cert.Extensions)
	}
	if cert.Extensions[ExtensionName] != "v1 write "+testLease+" 7 "+testExec {
		t.Fatalf("lease extension %q", cert.Extensions[ExtensionName])
	}
	for _, forbidden := range []string{"permit-port-forwarding", "permit-agent-forwarding", "permit-X11-forwarding", "permit-user-rc"} {
		if _, ok := cert.Extensions[forbidden]; ok {
			t.Fatalf("issued %s", forbidden)
		}
	}
	if cert.Serial != issued.Serial || issued.Serial == 0 || issued.Serial >= 1<<63 {
		t.Fatalf("serial %d", issued.Serial)
	}
	if issued.Fingerprint != ssh.FingerprintSHA256(pub) || string(cert.Key.Marshal()) != string(pub.Marshal()) {
		t.Fatal("certificate does not bind the client key")
	}
	if string(cert.SignatureKey.Marshal()) != string(a.ca.PublicKey().Marshal()) {
		t.Fatal("not signed by the CA")
	}
	if strings.Contains(issued.Line, testToken) || strings.Contains(string(cert.Marshal()), testToken) {
		t.Fatal("lease token reached the certificate")
	}
	other, err := a.IssueWriter(line, testClaims(), testNow)
	if err != nil || other.Serial == issued.Serial {
		t.Fatalf("serials repeat or error: %v", err)
	}
}

func TestIssueWriterAcceptsECDSAP256(t *testing.T) {
	a, _ := openTemp(t)
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sp, _ := ssh.NewPublicKey(&k.PublicKey)
	if _, err := a.IssueWriter(string(ssh.MarshalAuthorizedKey(sp)), testClaims(), testNow); err != nil {
		t.Fatal(err)
	}
}

func TestIssueWriterRefusesKeys(t *testing.T) {
	a, _ := openTemp(t)
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaKey, _ := ssh.NewPublicKey(&rk.PublicKey)
	var params dsa.Parameters
	if err := dsa.GenerateParameters(&params, rand.Reader, dsa.L1024N160); err != nil {
		t.Fatal(err)
	}
	dk := &dsa.PrivateKey{PublicKey: dsa.PublicKey{Parameters: params}}
	if err := dsa.GenerateKey(dk, rand.Reader); err != nil {
		t.Fatal(err)
	}
	dsaKey, _ := ssh.NewPublicKey(&dk.PublicKey)
	ek, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	p384, _ := ssh.NewPublicKey(&ek.PublicKey)
	_, good := clientKey(t)
	issued, err := a.IssueWriter(good, testClaims(), testNow)
	if err != nil {
		t.Fatal(err)
	}
	_, second := clientKey(t)
	cases := map[string]string{
		"ssh-rsa":       string(ssh.MarshalAuthorizedKey(rsaKey)),
		"ssh-dss":       string(ssh.MarshalAuthorizedKey(dsaKey)),
		"nistp384":      string(ssh.MarshalAuthorizedKey(p384)),
		"certificate":   issued.Line,
		"garbage":       "ssh-ed25519 notbase64!!",
		"empty":         "",
		"9 KiB":         strings.TrimSpace(good) + " " + strings.Repeat("c", 9*1024),
		"options":       `command="/bin/sh" ` + good,
		"two keys":      good + second,
		"CA key":        string(ssh.MarshalAuthorizedKey(a.ca.PublicKey())),
		"host key":      a.HostKeyLine(),
		"trailing junk": strings.TrimSpace(good) + "\nnot a key",
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := a.IssueWriter(line, testClaims(), testNow); !errors.Is(err, ErrKey) {
				t.Fatalf("IssueWriter = %v, want ErrKey", err)
			}
		})
	}
}

func TestIssueWriterRefusesClaims(t *testing.T) {
	a, _ := openTemp(t)
	_, line := clientKey(t)
	cases := map[string]func(*Claims){
		"short bench":       func(c *Claims) { c.BenchID = "abc" },
		"upper bench":       func(c *Claims) { c.BenchID = strings.ToUpper(testBench) },
		"spaced lease":      func(c *Claims) { c.LeaseID = testLease[:31] + " " },
		"token-length exec": func(c *Claims) { c.ExecutionID = testExec + "00" },
		"zero generation":   func(c *Claims) { c.Generation = 0 },
		"huge generation":   func(c *Claims) { c.Generation = 1 << 63 },
		"empty exec":        func(c *Claims) { c.ExecutionID = "" },
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			c := testClaims()
			spoil(&c)
			if _, err := a.IssueWriter(line, c, testNow); !errors.Is(err, ErrCertificate) {
				t.Fatalf("IssueWriter = %v, want ErrCertificate", err)
			}
		})
	}
}

func TestVerifyAcceptsOwnCertificate(t *testing.T) {
	a, _ := openTemp(t)
	_, line := clientKey(t)
	issued, err := a.IssueWriter(line, testClaims(), testNow)
	if err != nil {
		t.Fatal(err)
	}
	cert := parseLine(t, issued.Line)
	for _, at := range []time.Time{testNow, testNow.Add(-ClockSkew), testNow.Add(WriterValidity - time.Second)} {
		got, gotCert, err := a.Verify("b-"+testBench, cert, at)
		if err != nil {
			t.Fatalf("at %v: %v", at, err)
		}
		if got != testClaims() || gotCert != cert {
			t.Fatalf("claims %+v", got)
		}
	}
}

// resign copies the issued certificate, applies change and signs it with signer.
func resign(t *testing.T, base *ssh.Certificate, signer ssh.Signer, change func(*ssh.Certificate)) *ssh.Certificate {
	t.Helper()
	c := *base
	c.Permissions = ssh.Permissions{CriticalOptions: map[string]string{}, Extensions: map[string]string{}}
	for k, v := range base.Extensions {
		c.Extensions[k] = v
	}
	c.ValidPrincipals = append([]string(nil), base.ValidPrincipals...)
	change(&c)
	if err := c.SignCert(rand.Reader, signer); err != nil {
		t.Fatal(err)
	}
	return &c
}

func TestVerifyRefuses(t *testing.T) {
	a, _ := openTemp(t)
	other, _ := openTemp(t)
	pub, line := clientKey(t)
	issued, err := a.IssueWriter(line, testClaims(), testNow)
	if err != nil {
		t.Fatal(err)
	}
	base := parseLine(t, issued.Line)
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaKey, _ := ssh.NewPublicKey(&rk.PublicKey)
	user := "b-" + testBench
	same := func(*ssh.Certificate) {}
	ext := func(v string) func(*ssh.Certificate) {
		return func(c *ssh.Certificate) { c.Extensions[ExtensionName] = v }
	}
	cases := []struct {
		name string
		user string
		key  ssh.PublicKey
		now  time.Time
	}{
		{"other CA", user, resign(t, base, other.ca, same), testNow},
		{"host key as CA", user, resign(t, base, a.host, same), testNow},
		{"wrong bench", "b-" + testLease, base, testNow},
		{"upper-case user", "b-" + strings.ToUpper(testBench), resign(t, base, a.ca, func(c *ssh.Certificate) { c.ValidPrincipals = []string{"b-" + strings.ToUpper(testBench)} }), testNow},
		{"bare bench id user", testBench, base, testNow},
		{"expired", user, base, testNow.Add(WriterValidity)},
		{"not yet valid", user, base, testNow.Add(-ClockSkew - time.Second)},
		{"host cert type", user, resign(t, base, a.ca, func(c *ssh.Certificate) { c.CertType = ssh.HostCert }), testNow},
		{"two principals", user, resign(t, base, a.ca, func(c *ssh.Certificate) { c.ValidPrincipals = append(c.ValidPrincipals, "b-"+testLease) }), testNow},
		{"no principals", user, resign(t, base, a.ca, func(c *ssh.Certificate) { c.ValidPrincipals = nil }), testNow},
		{"missing extension", user, resign(t, base, a.ca, func(c *ssh.Certificate) { delete(c.Extensions, ExtensionName) }), testNow},
		{"generation 0", user, resign(t, base, a.ca, ext("v1 write "+testLease+" 0 "+testExec)), testNow},
		{"generation 2^63", user, resign(t, base, a.ca, ext("v1 write "+testLease+" "+strconv.FormatUint(1<<63, 10)+" "+testExec)), testNow},
		{"generation leading zero", user, resign(t, base, a.ca, ext("v1 write "+testLease+" 07 "+testExec)), testNow},
		{"generation plus", user, resign(t, base, a.ca, ext("v1 write "+testLease+" +7 "+testExec)), testNow},
		{"mode read", user, resign(t, base, a.ca, ext("v1 read "+testLease+" 7 "+testExec)), testNow},
		{"version v2", user, resign(t, base, a.ca, ext("v2 write "+testLease+" 7 "+testExec)), testNow},
		{"six fields", user, resign(t, base, a.ca, ext("v1 write "+testLease+" 7 "+testExec+" x")), testNow},
		{"double space", user, resign(t, base, a.ca, ext("v1 write  "+testLease+" 7 "+testExec)), testNow},
		{"short execution", user, resign(t, base, a.ca, ext("v1 write "+testLease+" 7 abc")), testNow},
		{"critical option", user, resign(t, base, a.ca, func(c *ssh.Certificate) { c.CriticalOptions["force-command"] = "/bin/true" }), testNow},
		{"port forwarding", user, resign(t, base, a.ca, func(c *ssh.Certificate) { c.Extensions["permit-port-forwarding"] = "" }), testNow},
		{"agent forwarding", user, resign(t, base, a.ca, func(c *ssh.Certificate) { c.Extensions["permit-agent-forwarding"] = "" }), testNow},
		{"pty with value", user, resign(t, base, a.ca, func(c *ssh.Certificate) { c.Extensions["permit-pty"] = "x" }), testNow},
		{"key id differs", user, resign(t, base, a.ca, func(c *ssh.Certificate) { c.KeyId = testExec }), testNow},
		{"infinite validity", user, resign(t, base, a.ca, func(c *ssh.Certificate) { c.ValidBefore = ssh.CertTimeInfinity }), testNow},
		{"long window", user, resign(t, base, a.ca, func(c *ssh.Certificate) { c.ValidBefore += 3600 }), testNow},
		{"tampered after signing", user, func() *ssh.Certificate { c := *base; c.KeyId = testExec; return &c }(), testNow},
		{"rsa certified key", user, resign(t, base, a.ca, func(c *ssh.Certificate) { c.Key = rsaKey }), testNow},
		{"plain public key", user, pub, testNow},
		{"plain CA key", user, a.ca.PublicKey(), testNow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := a.Verify(tc.user, tc.key, tc.now); !errors.Is(err, ErrCertificate) {
				t.Fatalf("Verify = %v, want ErrCertificate", err)
			}
		})
	}
	if _, _, err := a.Verify(user, base, testNow); err != nil {
		t.Fatalf("base certificate refused: %v", err)
	}
}
