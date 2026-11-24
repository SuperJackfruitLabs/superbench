package remoteca

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	ExtensionName  = "superbench-lease@superbench.dev"
	WriterValidity = 30 * time.Minute
	ClockSkew      = time.Minute // ValidAfter = now - ClockSkew
	permitPTY      = "permit-pty"
	maxSigned      = 1<<63 - 1 // Serials and generations fit SQLite's signed INTEGER.
)

var (
	userPattern = regexp.MustCompile(`^b-[a-f0-9]{32}$`)
	hex32       = regexp.MustCompile(`^[a-f0-9]{32}$`)
)

// Claims are what a writer certificate binds. They carry the lease's opaque
// ID and generation, never its token.
type Claims struct {
	BenchID, LeaseID string
	Generation       uint64
	ExecutionID      string
}

// Principal is the single SSH user and principal for a bench.
func Principal(benchID string) string { return "b-" + benchID }

// Issued is a signed writer certificate in authorized_keys form, with the
// client key's fingerprint.
type Issued struct {
	Line                    string
	Serial                  uint64
	Fingerprint             string
	ValidAfter, ValidBefore time.Time
}

// IssueWriter signs a 30-minute writer certificate for the client key.
func (a *Authority) IssueWriter(authorizedKey string, c Claims, now time.Time) (Issued, error) {
	key, err := parseClientKey(authorizedKey)
	if err != nil {
		return Issued{}, err
	}
	if m := key.Marshal(); bytes.Equal(m, a.ca.PublicKey().Marshal()) || bytes.Equal(m, a.host.PublicKey().Marshal()) {
		return Issued{}, fmt.Errorf("%w: client key is one of benchd's own keys", ErrKey)
	}
	if !hex32.MatchString(c.BenchID) || !hex32.MatchString(c.LeaseID) || !hex32.MatchString(c.ExecutionID) || c.Generation < 1 || c.Generation > maxSigned {
		return Issued{}, fmt.Errorf("%w: invalid claims", ErrCertificate)
	}
	after, before := now.Add(-ClockSkew), now.Add(WriterValidity)
	if after.Unix() <= 0 {
		return Issued{}, fmt.Errorf("%w: clock before the epoch", ErrCertificate)
	}
	serial, err := randomSerial()
	if err != nil {
		return Issued{}, err
	}
	cert := &ssh.Certificate{
		Key:             key,
		Serial:          serial,
		CertType:        ssh.UserCert,
		KeyId:           c.LeaseID,
		ValidPrincipals: []string{Principal(c.BenchID)},
		ValidAfter:      uint64(after.Unix()),
		ValidBefore:     uint64(before.Unix()),
		Permissions: ssh.Permissions{
			CriticalOptions: map[string]string{},
			Extensions:      map[string]string{permitPTY: "", ExtensionName: formatExtension(c)},
		},
	}
	if err := cert.SignCert(rand.Reader, a.ca); err != nil {
		return Issued{}, err
	}
	// Never hand out what Verify would refuse.
	if _, _, err := a.Verify(Principal(c.BenchID), cert, now); err != nil {
		return Issued{}, err
	}
	return Issued{
		Line:        strings.TrimSpace(string(ssh.MarshalAuthorizedKey(cert))),
		Serial:      serial,
		Fingerprint: ssh.FingerprintSHA256(key),
		ValidAfter:  time.Unix(int64(cert.ValidAfter), 0),
		ValidBefore: time.Unix(int64(cert.ValidBefore), 0),
	}, nil
}

// Verify accepts only a user certificate signed by this CA, for exactly the
// one principal user, inside its window, with no critical options, no
// extension but permit-pty and the lease extension, and a well-formed lease
// extension whose lease ID is the key ID. Lease state is the caller's check.
func (a *Authority) Verify(user string, key ssh.PublicKey, now time.Time) (Claims, *ssh.Certificate, error) {
	refuse := func(why string) (Claims, *ssh.Certificate, error) {
		return Claims{}, nil, fmt.Errorf("%w: %s", ErrCertificate, why)
	}
	cert, ok := key.(*ssh.Certificate)
	if !ok || cert == nil {
		return refuse("not a certificate")
	}
	if cert.CertType != ssh.UserCert || cert.SignatureKey == nil || !bytes.Equal(cert.SignatureKey.Marshal(), a.ca.PublicKey().Marshal()) {
		return refuse("not a user certificate from this CA")
	}
	if !userPattern.MatchString(user) || len(cert.ValidPrincipals) != 1 || cert.ValidPrincipals[0] != user {
		return refuse("principal does not name this bench")
	}
	if len(cert.CriticalOptions) != 0 {
		return refuse("critical options present")
	}
	for name, value := range cert.Extensions {
		if name != permitPTY && name != ExtensionName || name == permitPTY && value != "" {
			return refuse("unexpected extension")
		}
	}
	checker := ssh.CertChecker{Clock: func() time.Time { return now }}
	if err := checker.CheckCert(user, cert); err != nil {
		return refuse("signature or validity window")
	}
	if cert.ValidBefore == ssh.CertTimeInfinity || cert.ValidBefore <= cert.ValidAfter || cert.ValidBefore-cert.ValidAfter > uint64((WriterValidity+ClockSkew)/time.Second) {
		return refuse("validity window too long")
	}
	if t := cert.Key.Type(); t != ssh.KeyAlgoED25519 && t != ssh.KeyAlgoECDSA256 {
		return refuse("certified key type")
	}
	c, ok := parseExtension(cert.Extensions[ExtensionName])
	if !ok || cert.KeyId != c.LeaseID {
		return refuse("lease extension")
	}
	c.BenchID = user[2:]
	return c, cert, nil
}

func formatExtension(c Claims) string {
	return "v1 write " + c.LeaseID + " " + strconv.FormatUint(c.Generation, 10) + " " + c.ExecutionID
}

// parseExtension reads exactly "v1 write <lease> <generation> <execution>".
func parseExtension(v string) (Claims, bool) {
	f := strings.Split(v, " ")
	if len(f) != 5 || f[0] != "v1" || f[1] != "write" || !hex32.MatchString(f[2]) || !hex32.MatchString(f[4]) {
		return Claims{}, false
	}
	g, err := strconv.ParseUint(f[3], 10, 64)
	if err != nil || g < 1 || g > maxSigned || strconv.FormatUint(g, 10) != f[3] {
		return Claims{}, false
	}
	return Claims{LeaseID: f[2], Generation: g, ExecutionID: f[4]}, true
}

func randomSerial() (uint64, error) {
	var b [8]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		if s := binary.BigEndian.Uint64(b[:]) & maxSigned; s != 0 {
			return s, nil
		}
	}
}
