package hoststorage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unicode/utf8"
)

const MaxStorageIdentity = 65536
const storageVersion = "superbench.storage-identity.v1"

// StorageIdentity binds an existing physical data root. Managed identities also
// pin its dedicated filesystem, fixed policy, existing ledger and owner anchor.
// It is evidence to recheck, never permission to create or adopt a directory.
type StorageIdentity struct {
	Version, Root, BenchKey                                                string
	Device, Inode                                                          uint64
	Mount                                                                  string
	Managed                                                                bool
	FilesystemUUID, PolicySHA256, LedgerSHA256, AnchorSHA256, ConfigSHA256 string `json:",omitempty"`
	Assignment                                                             Assignment
}

func (s StorageIdentity) validate() error {
	if s.Version != storageVersion || !Key.MatchString(s.BenchKey) || !utf8.ValidString(s.Root) || !utf8.ValidString(s.Mount) || !filepath.IsAbs(s.Root) || filepath.Clean(s.Root) != s.Root || s.Root == "/" || s.Device == 0 || s.Inode == 0 || s.Mount == "" || len(s.Mount) > 8192 || s.Assignment.Path != filepath.Join(s.Root, "benches", s.BenchKey) {
		return errors.New("invalid storage identity")
	}
	if s.Managed {
		if !volumePattern.MatchString(s.FilesystemUUID) || s.Assignment.ProjectID < 10000 || s.Assignment.Bytes == 0 || s.Assignment.Inodes == 0 {
			return errors.New("invalid managed storage identity")
		}
		for _, digest := range []string{s.PolicySHA256, s.LedgerSHA256, s.AnchorSHA256, s.ConfigSHA256} {
			decoded, err := hex.DecodeString(digest)
			if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != digest {
				return errors.New("invalid storage digest")
			}
		}
	} else if s.FilesystemUUID != "" || s.PolicySHA256 != "" || s.LedgerSHA256 != "" || s.AnchorSHA256 != "" || s.ConfigSHA256 != "" || s.Assignment.ProjectID != 0 || s.Assignment.Bytes != 0 || s.Assignment.Inodes != 0 {
		return errors.New("unmanaged storage cannot claim managed evidence")
	}
	return nil
}

func (s StorageIdentity) Bytes() ([]byte, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(s)
	if len(raw) > MaxStorageIdentity {
		return nil, errors.New("storage identity exceeds limit")
	}
	return raw, err
}

func ParseStorageIdentity(raw []byte) (StorageIdentity, error) {
	var s StorageIdentity
	if len(raw) > MaxStorageIdentity {
		return s, errors.New("storage identity exceeds limit")
	}
	if err := strictStorageJSON(raw, &s); err != nil {
		return StorageIdentity{}, err
	}
	canonical, err := s.Bytes()
	if err != nil {
		return StorageIdentity{}, err
	}
	if !bytes.Equal(raw, canonical) {
		return StorageIdentity{}, errors.New("storage identity is not canonical")
	}
	return s, nil
}

func strictStorageJSON(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("trailing storage JSON")
	}
	return nil
}

func storageDigest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

func physicalStorageRoot(root string) (uint64, uint64, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
		return 0, 0, errors.New("storage root must be an existing physical absolute directory")
	}
	physical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return 0, 0, err
	}
	if physical != root {
		return 0, 0, errors.New("storage root contains a symlink")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return 0, 0, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return 0, 0, errors.New("storage root is not a directory")
	}
	st := info.Sys().(*syscall.Stat_t)
	return uint64(st.Dev), st.Ino, nil
}

// InspectStorage never allocates, creates a missing root, repairs an anchor or
// falls back to an existing ancestor. expected comes from the durable bench row.
func (w Worker) InspectStorage(ctx context.Context, key string, expected Assignment) (StorageIdentity, error) {
	return w.inspectStorage(ctx, key, expected, storageCommand)
}

func (w Worker) inspectStorage(ctx context.Context, key string, expected Assignment, command func(context.Context, string, ...string) ([]byte, error)) (StorageIdentity, error) {
	var zero StorageIdentity
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if !Key.MatchString(key) || expected.Path != filepath.Join(w.Data, "benches", key) {
		return zero, errors.New("storage assignment does not match bench root")
	}
	device, inode, err := physicalStorageRoot(w.Data)
	if err != nil {
		return zero, err
	}
	c, err := w.inspectStorageConfig()
	if err != nil {
		return zero, err
	}
	s := StorageIdentity{Version: storageVersion, Root: w.Data, BenchKey: key, Device: device, Inode: inode, Assignment: expected}
	if c == nil {
		s.Mount, err = unmanagedStorageMount(w.Data)
	} else {
		args := []string{}
		if c.PolicyOverride != "" {
			args = append(args, "--policy", c.PolicyOverride)
		}
		args = append(args, "verify", "bench", key)
		binary := c.QuotaHelper
		if os.Geteuid() != 0 {
			args = append([]string{"-n", binary}, args...)
			binary = "/usr/bin/sudo"
		}
		raw, e := command(ctx, binary, args...)
		if e != nil {
			return zero, e
		}
		if len(raw) > MaxStorageIdentity {
			return zero, errors.New("storage helper output exceeds limit")
		}
		s = StorageIdentity{}
		if err = strictStorageJSON(raw, &s); err != nil {
			return zero, err
		}
		canonical, _ := json.Marshal(s)
		if !bytes.Equal(raw, canonical) {
			return zero, errors.New("storage helper output is not canonical")
		}
		if !s.Managed || s.Root != w.Data || s.BenchKey != key || s.Device != device || s.Inode != inode || s.Assignment != expected || s.ConfigSHA256 != "" {
			return zero, errors.New("quota verifier returned different storage authority")
		}
		config, _ := json.Marshal(c)
		s.ConfigSHA256 = storageDigest(config)
	}
	if err != nil {
		return zero, err
	}
	if err = s.validate(); err != nil {
		return zero, err
	}
	endDevice, endInode, err := physicalStorageRoot(w.Data)
	if err != nil {
		return zero, err
	}
	if endDevice != device || endInode != inode {
		return zero, errors.New("storage root changed during inspection")
	}
	if c == nil {
		endMount, e := unmanagedStorageMount(w.Data)
		if e != nil {
			return zero, e
		}
		if endMount != s.Mount {
			return zero, errors.New("storage mount changed during inspection")
		}
	}
	if err = ctx.Err(); err != nil {
		return zero, err
	}
	return s, nil
}

func (w Worker) inspectStorageConfig() (*HostConfig, error) {
	f, err := os.OpenFile(filepath.Join(w.State, "host.json"), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("host policy is not a regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, MaxStorageIdentity+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxStorageIdentity {
		return nil, errors.New("host policy exceeds storage verification limit")
	}
	var c HostConfig
	if err = strictStorageJSON(raw, &c); err != nil {
		return nil, err
	}
	c = c.Defaults()
	return &c, c.Validate(w.Data)
}

func (w Worker) VerifyStorage(ctx context.Context, s StorageIdentity) error {
	if err := s.validate(); err != nil {
		return err
	}
	if s.Root != w.Data {
		return errors.New("storage proof belongs to another data root")
	}
	current, err := w.InspectStorage(ctx, s.BenchKey, s.Assignment)
	if err != nil {
		return err
	}
	if current != s {
		return errors.New("storage identity changed since inspection")
	}
	return nil
}

type storageOutput struct {
	buffer   bytes.Buffer
	overflow bool
	stop     context.CancelFunc
}

func (b *storageOutput) Write(raw []byte) (int, error) {
	if len(raw) > MaxStorageIdentity-b.buffer.Len() {
		b.overflow = true
		if b.stop != nil {
			b.stop()
		}
		return 0, errors.New("storage command output exceeds limit")
	}
	return b.buffer.Write(raw)
}

func storageCommand(ctx context.Context, binary string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	var stdout, stderr storageOutput
	stdout.stop, stderr.stop = cancel, cancel
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if stdout.overflow || stderr.overflow {
		return nil, errors.New("storage command output exceeds limit")
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("read-only storage verification command failed")
	}
	return stdout.buffer.Bytes(), nil
}
