// Package remoteca is benchd's SSH certificate authority for remote access to
// benches: it owns the CA and host keys and issues and verifies short-lived
// writer certificates. Nothing here runs on import; Open is the only creator
// of key material and is called only when remote access is on.
package remoteca

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/crypto/ssh"
)

const (
	caFile      = "ca_ed25519"
	hostFile    = "host_ed25519"
	maxKeyFile  = 16 << 10
	maxClientIn = 8 << 10
)

var (
	ErrKey         = errors.New("remoteca: key refused")
	ErrCertificate = errors.New("remoteca: certificate refused")
)

// Authority holds the CA and host signers. The private keys stay in this
// process and in their 0600 files; no method returns the CA signer.
type Authority struct {
	ca   ssh.Signer
	host ssh.Signer
}

// Open loads the CA and host keys from dir (<state>/remote), creating the
// directory (0700) and any missing key (0600). Unsafe files refuse with ErrKey.
func Open(dir string) (*Authority, error) {
	if err := ensureDir(dir); err != nil {
		return nil, err
	}
	ca, err := loadOrCreate(dir, caFile)
	if err != nil {
		return nil, err
	}
	host, err := loadOrCreate(dir, hostFile)
	if err != nil {
		return nil, err
	}
	if string(ca.PublicKey().Marshal()) == string(host.PublicKey().Marshal()) {
		return nil, fmt.Errorf("%w: CA and host key are the same key", ErrKey)
	}
	return &Authority{ca: ca, host: host}, nil
}

func (a *Authority) HostSigner() ssh.Signer { return a.host }

func (a *Authority) HostKeyLine() string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(a.host.PublicKey())))
}

func (a *Authority) HostFingerprint() string { return ssh.FingerprintSHA256(a.host.PublicKey()) }

// Fingerprint returns the SHA256 fingerprint of an accepted client key line.
func Fingerprint(authorizedKey string) (string, error) {
	k, err := parseClientKey(authorizedKey)
	if err != nil {
		return "", err
	}
	return ssh.FingerprintSHA256(k), nil
}

// parseClientKey accepts exactly one bare ssh-ed25519 or ecdsa-sha2-nistp256
// public key line, at most 8 KiB, with no options and no certificate. An
// ed25519 key must decode to a point outside the small-order subgroup.
func parseClientKey(line string) (ssh.PublicKey, error) {
	if len(line) > maxClientIn {
		return nil, fmt.Errorf("%w: client key over 8 KiB", ErrKey)
	}
	k, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return nil, fmt.Errorf("%w: client key does not parse", ErrKey)
	}
	if len(options) != 0 || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, fmt.Errorf("%w: client key line has options or more keys", ErrKey)
	}
	if _, ok := k.(*ssh.Certificate); ok {
		return nil, fmt.Errorf("%w: client key is a certificate", ErrKey)
	}
	if t := k.Type(); t != ssh.KeyAlgoED25519 && t != ssh.KeyAlgoECDSA256 {
		return nil, fmt.Errorf("%w: client key type %q not accepted", ErrKey, t)
	}
	if c, ok := k.(ssh.CryptoPublicKey); ok {
		if ed, ok := c.CryptoPublicKey().(ed25519.PublicKey); ok && smallOrderEd25519(ed) {
			return nil, fmt.Errorf("%w: client key is not a valid ed25519 point or has small order", ErrKey)
		}
	}
	return k, nil
}

func ensureDir(dir string) error {
	err := os.Mkdir(dir, 0o700)
	if err == nil {
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
		if err := syncDir(filepath.Dir(dir)); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode()&os.ModeType != os.ModeDir || st.Mode().Perm() != 0o700 || st.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return fmt.Errorf("%w: %s must be a real directory with mode 0700", ErrKey, dir)
	}
	if err := ownedByMe(st); err != nil {
		return err
	}
	return nil
}

func loadOrCreate(dir, name string) (ssh.Signer, error) {
	p := filepath.Join(dir, name)
	s, err := load(p)
	if !errors.Is(err, os.ErrNotExist) {
		return s, err
	}
	err = create(p)
	if errors.Is(err, os.ErrExist) {
		return load(p) // Another Open won the race; its file must pass the same checks.
	}
	if err != nil {
		return nil, err
	}
	return load(p)
}

func load(p string) (ssh.Signer, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s: %v", ErrKey, filepath.Base(p), err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Mode()&os.ModeType != 0 || st.Mode().Perm() != 0o600 || st.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return nil, fmt.Errorf("%w: %s must be a regular file with mode 0600", ErrKey, filepath.Base(p))
	}
	if err := ownedByMe(st); err != nil {
		return nil, err
	}
	if sys := st.Sys().(*syscall.Stat_t); uint64(sys.Nlink) != 1 {
		return nil, fmt.Errorf("%w: %s has more than one link", ErrKey, filepath.Base(p))
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxKeyFile+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxKeyFile {
		return nil, fmt.Errorf("%w: %s is too large", ErrKey, filepath.Base(p))
	}
	key, err := ssh.ParseRawPrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %s is not an OpenSSH private key", ErrKey, filepath.Base(p))
	}
	var priv ed25519.PrivateKey
	switch k := key.(type) {
	case ed25519.PrivateKey:
		priv = k
	case *ed25519.PrivateKey:
		priv = *k
	default:
		return nil, fmt.Errorf("%w: %s is not an ed25519 key", ErrKey, filepath.Base(p))
	}
	if len(priv) != ed25519.PrivateKeySize || !priv.Equal(ed25519.NewKeyFromSeed(priv.Seed())) {
		return nil, fmt.Errorf("%w: %s is malformed", ErrKey, filepath.Base(p))
	}
	return ssh.NewSignerFromKey(priv)
}

// create writes a fresh ed25519 key with O_EXCL. A crash mid-write leaves a
// file that load refuses; the operator removes it. It never overwrites.
func create(p string) error {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	err = f.Chmod(0o600)
	if err == nil {
		_, err = f.Write(pem.EncodeToMemory(block))
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return syncDir(filepath.Dir(p))
}

func ownedByMe(st os.FileInfo) error {
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || int(sys.Uid) != os.Geteuid() {
		return fmt.Errorf("%w: %s is not owned by this process's user", ErrKey, st.Name())
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}
