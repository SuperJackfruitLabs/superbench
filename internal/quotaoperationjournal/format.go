package quotaoperationjournal

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

const formatVersion = "superbench.quota-journal.v2"

type formatIntent struct {
	Version string `json:"version"`
	Ledger  string `json:"ledger"`
	Nonce   string `json:"nonce"`
}

type formatReady struct {
	Device         uint64 `json:"device"`
	Inode          uint64 `json:"inode"`
	LockDevice     uint64 `json:"lock_device"`
	LockInode      uint64 `json:"lock_inode"`
	DatabaseDevice uint64 `json:"database_device"`
	DatabaseInode  uint64 `json:"database_inode"`
	Authority      string `json:"authority"`
}

// ErrReadyAuthority is a ready record whose pinned storage authority differs
// from the current one. A ledger initialized before the authority stopped
// hashing the mount ID always gets it; it is never adopted or rewritten.
var ErrReadyAuthority = fmt.Errorf("%w: the ready record pins a storage authority that the mounted data volume, storage policy and ledger no longer match; "+
	"a ledger initialized by an earlier SuperBench revision (which pinned the mount ID) must be reinitialized, see \"Reinitialize the indexed ledger\" in docs/pilot-installation.md", ErrAuthority)

func formatPath(directory string) string { return directory + ".format-v2" }

func validDigest(raw string) bool {
	decoded, err := hex.DecodeString(raw)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == raw
}

func validConfig(cfg Config, owner uint32) bool {
	return cfg.CheckControl != nil && cfg.CheckReady != nil && filepath.IsAbs(cfg.Directory) && filepath.Clean(cfg.Directory) == cfg.Directory && cfg.Directory != "/" && uint32(os.Geteuid()) == owner
}

// OpenExisting requires complete retained format authority; missing or partial
// history never grants first-install permission. No operation identity is used.
func OpenExisting(ctx context.Context, cfg Config) (*Control, error) {
	return openExisting(ctx, cfg, 0)
}

func openExisting(ctx context.Context, cfg Config, owner uint32) (*Control, error) {
	if !validConfig(cfg, owner) {
		return nil, ErrAuthority
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := cfg.CheckControl(ctx); err != nil {
		return nil, err
	}
	parent, err := directory(filepath.Dir(cfg.Directory), owner)
	if err != nil {
		return nil, err
	}
	ancestors, err := ancestry(cfg.Directory, owner)
	if err != nil {
		return nil, err
	}
	f, err := openFile(formatPath(cfg.Directory), owner, false)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(f, 2049))
	if err != nil {
		return nil, err
	}
	ready, err := parseFormat(cfg.Directory, raw)
	if err != nil {
		return nil, err
	}
	li, err := directory(cfg.Directory, owner)
	if err != nil {
		return nil, err
	}
	st := li.Sys().(*syscall.Stat_t)
	if ready.Device != uint64(st.Dev) || ready.Inode != uint64(st.Ino) {
		return nil, ErrAuthority
	}
	pins := func() error {
		if err := ancestors(); err != nil {
			return err
		}
		now, err := directory(filepath.Dir(cfg.Directory), owner)
		if err != nil || !os.SameFile(parent, now) {
			return ErrAuthority
		}
		ledger, err := directory(cfg.Directory, owner)
		if err != nil || !os.SameFile(li, ledger) {
			return ErrAuthority
		}
		if err := checkAnchor(cfg.Directory, owner, fi, raw); err != nil {
			return err
		}
		return checkFormatObjects(cfg.Directory, owner, ready)
	}
	c := &Control{config: cfg, owner: owner, authority: ready.Authority}
	c.verify = func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := cfg.CheckControl(ctx); err != nil {
			return err
		}
		if err := pins(); err != nil {
			return err
		}
		actual, err := cfg.CheckReady(ctx)
		if err != nil {
			return err
		}
		if !validDigest(actual) {
			return ErrAuthority
		}
		if actual != ready.Authority {
			return ErrReadyAuthority
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return pins()
	}
	if err := c.Run(ctx, func() error { return nil }); err != nil {
		return nil, err
	}
	return c, nil
}

func parseFormat(directory string, raw []byte) (formatReady, error) {
	var intent formatIntent
	var ready formatReady
	parts := bytes.Split(raw, []byte{'\n'})
	if len(raw) > 2048 || len(parts) != 3 || len(parts[2]) != 0 {
		return ready, ErrAuthority
	}
	if json.Unmarshal(parts[0], &intent) != nil || json.Unmarshal(parts[1], &ready) != nil {
		return ready, ErrAuthority
	}
	ci, _ := json.Marshal(intent)
	cr, _ := json.Marshal(ready)
	if !validDigest(intent.Nonce) || intent.Version != formatVersion || intent.Ledger != directory || !bytes.Equal(ci, parts[0]) || !bytes.Equal(cr, parts[1]) || !validDigest(ready.Authority) {
		return formatReady{}, ErrAuthority
	}
	return ready, nil
}

func checkAnchor(directory string, owner uint32, info os.FileInfo, expected []byte) error {
	f, err := openFile(formatPath(directory), owner, false)
	if err != nil {
		return err
	}
	defer f.Close()
	now, err := f.Stat()
	if err != nil || !os.SameFile(info, now) {
		return ErrAuthority
	}
	raw, err := io.ReadAll(io.LimitReader(f, 2049))
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, expected) {
		return ErrAuthority
	}
	return nil
}

func checkFormatObjects(directory string, owner uint32, ready formatReady) error {
	for path, expected := range map[string][2]uint64{
		filepath.Join(directory, "operations.lock"):                        {ready.LockDevice, ready.LockInode},
		filepath.Join(directory, "operations", "indexed-v2", "journal.db"): {ready.DatabaseDevice, ready.DatabaseInode},
	} {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !private(info, owner, false) {
			return ErrAuthority
		}
		st := info.Sys().(*syscall.Stat_t)
		if uint64(st.Dev) != expected[0] || uint64(st.Ino) != expected[1] {
			return ErrAuthority
		}
	}
	return nil
}

// InitializeFresh invokes only the fixed nonallocating bootstrap while retaining
// operations.lock. Bootstrap success is not readiness: actual CheckReady must
// succeed before any indexed database or complete anchor is published.
func (init *Initializer) InitializeFresh(ctx context.Context, bootstrap func(context.Context, FreshLedger) error) (*Control, error) {
	if init == nil || bootstrap == nil || init.config.Authorize == nil || !validConfig(init.config.Control, init.owner) {
		return nil, ErrAuthority
	}
	if init.owner == 0 && (os.Getuid() != 0 || os.Getenv("SUDO_UID") != "") {
		return nil, ErrAuthority
	}
	cfg := init.config.Control
	parent := filepath.Dir(cfg.Directory)
	pi, err := directory(parent, init.owner)
	if err != nil {
		return nil, err
	}
	ancestors, err := ancestry(parent, init.owner)
	if err != nil {
		return nil, err
	}
	parentCheck := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := ancestors(); err != nil {
			return err
		}
		now, err := directory(parent, init.owner)
		if err != nil || !os.SameFile(pi, now) {
			return ErrAuthority
		}
		return cfg.CheckControl(ctx)
	}
	if err = parentCheck(); err != nil {
		return nil, err
	}
	if err = init.config.Authorize(ctx); err != nil {
		return nil, err
	}
	if err = parentCheck(); err != nil {
		return nil, err
	}
	if _, err = os.Lstat(cfg.Directory); err == nil {
		return nil, os.ErrExist
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	f, err := openFile(formatPath(cfg.Directory), init.owner, true)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	var nonce [32]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(formatIntent{formatVersion, cfg.Directory, hex.EncodeToString(nonce[:])})
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	if _, err = f.Write(raw); err != nil {
		return nil, err
	}
	if err = f.Sync(); err != nil {
		return nil, err
	}
	if err = syncDirectory(parent); err != nil {
		return nil, err
	}
	if err = parentCheck(); err != nil {
		return nil, err
	}
	if err = os.Mkdir(cfg.Directory, 0700); err != nil {
		return nil, err
	}
	if err = syncDirectory(parent); err != nil {
		return nil, err
	}
	li, err := directory(cfg.Directory, init.owner)
	if err != nil {
		return nil, err
	}
	session := freshSession{config: cfg, owner: init.owner, file: f, anchor: fi, ledger: li, expected: raw, parentCheck: parentCheck}
	return session.initialize(ctx, bootstrap)
}
