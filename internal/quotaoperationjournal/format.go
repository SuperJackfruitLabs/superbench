package quotaoperationjournal

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
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
}

// The sibling anchor survives loss of operations/ or the whole ledger. Its
// incomplete first record is permanent refusal evidence, not a resume ticket.
func (s Store) formatPath() string { return s.Directory + ".format-v2" }

// InitializeFresh is an explicit installer action, never an Apply/Cancel
// fallback. Both the anchor and ledger must be exclusively created by this
// invocation. Failure retains every created file and never rolls history back.
// The supplied Check must independently authorize first installation, including
// the fixed policy and mounted root; this method grants no such authority.
func (s Store) InitializeFresh(ctx context.Context) (Store, error) {
	if s.Check == nil || !s.Identity.Valid() || uint32(os.Geteuid()) != s.owner || s.finishFresh != nil {
		return Store{}, ErrAuthority
	}
	parent := filepath.Dir(s.Directory)
	pi, err := directory(parent, s.owner)
	if err != nil {
		return Store{}, err
	}
	ancestors, err := ancestry(parent, s.owner)
	if err != nil {
		return Store{}, err
	}
	checkParent := func() error {
		if err := ancestors(); err != nil {
			return err
		}
		now, err := directory(parent, s.owner)
		if err != nil || !os.SameFile(pi, now) {
			return ErrAuthority
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return s.Check(s.Identity)
	}
	if !filepath.IsAbs(s.Directory) || filepath.Clean(s.Directory) != s.Directory || s.Directory == "/" {
		return Store{}, ErrAuthority
	}
	if err = checkParent(); err != nil {
		return Store{}, err
	}
	// Refuse historical empty ledgers before writing an anchor, as well as any
	// retained anchor when the ledger has vanished. No stat result authorizes
	// initialization: the two subsequent exclusive creations are mandatory.
	if _, err = os.Lstat(s.Directory); err == nil {
		return Store{}, os.ErrExist
	}
	if !os.IsNotExist(err) {
		return Store{}, err
	}
	f, err := openFile(s.formatPath(), s.owner, true)
	if err != nil {
		return Store{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Store{}, err
	}
	var nonce [32]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return Store{}, err
	}
	intent, err := json.Marshal(formatIntent{formatVersion, s.Directory, hex.EncodeToString(nonce[:])})
	if err != nil {
		return Store{}, err
	}
	intent = append(intent, '\n')
	if _, err = f.Write(intent); err != nil {
		return Store{}, err
	}
	if err = f.Sync(); err != nil {
		return Store{}, err
	}
	if err = syncDirectory(parent); err != nil {
		return Store{}, err
	}
	if err = checkParent(); err != nil {
		return Store{}, err
	}
	if err = os.Mkdir(s.Directory, 0700); err != nil {
		return Store{}, err
	}
	if err = syncDirectory(parent); err != nil {
		return Store{}, err
	}
	li, err := directory(s.Directory, s.owner)
	if err != nil {
		return Store{}, err
	}
	return s.finishInitialization(ctx, f, fi, li, intent, checkParent)
}

func (s Store) finishInitialization(ctx context.Context, f *os.File, fi, li os.FileInfo, intent []byte, checkParent func() error) (Store, error) {
	initial := s
	expected := intent
	initial.Check = func(i quotaoperation.Identity) error {
		if i != s.Identity {
			return ErrAuthority
		}
		if err := checkParent(); err != nil {
			return err
		}
		now, err := os.Lstat(s.formatPath())
		if err != nil || !private(now, s.owner, false) || !os.SameFile(fi, now) {
			return ErrAuthority
		}
		ledger, err := directory(s.Directory, s.owner)
		if err != nil || !os.SameFile(li, ledger) {
			return ErrAuthority
		}
		if _, err = f.Seek(0, io.SeekStart); err != nil {
			return err
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
	initial.finishFresh = func() error {
		if err := initial.Check(s.Identity); err != nil {
			return err
		}
		state := li.Sys().(*syscall.Stat_t)
		lockInfo, err := os.Lstat(filepath.Join(s.Directory, "operations.lock"))
		if err != nil {
			return err
		}
		databaseInfo, err := os.Lstat(filepath.Join(s.Directory, "operations", "indexed-v2", "journal.db"))
		if err != nil {
			return err
		}
		if !private(lockInfo, s.owner, false) || !private(databaseInfo, s.owner, false) {
			return ErrAuthority
		}
		lockState := lockInfo.Sys().(*syscall.Stat_t)
		databaseState := databaseInfo.Sys().(*syscall.Stat_t)
		ready, err := json.Marshal(formatReady{uint64(state.Dev), uint64(state.Ino), uint64(lockState.Dev), uint64(lockState.Ino), uint64(databaseState.Dev), uint64(databaseState.Ino)})
		if err != nil {
			return err
		}
		ready = append(ready, '\n')
		// The preceding check leaves f exactly at the end of the pinned intent.
		if _, err := f.Write(ready); err != nil {
			return err
		}
		if err := f.Sync(); err != nil {
			return err
		}
		expected = append(append([]byte(nil), intent...), ready...)
		return initial.Check(s.Identity)
	}
	if err := initial.create(ctx, func(*Journal) error { return nil }); err != nil {
		return Store{}, err
	}
	return s.OpenExisting(ctx)
}

// OpenExisting requires a complete retained format anchor and pins its exact
// bytes, inode and ledger identity into every subsequent authority check.
func (s Store) OpenExisting(ctx context.Context) (Store, error) {
	if s.Check == nil || !s.Identity.Valid() || uint32(os.Geteuid()) != s.owner {
		return Store{}, ErrAuthority
	}
	parent, err := directory(filepath.Dir(s.Directory), s.owner)
	if err != nil {
		return Store{}, err
	}
	ancestors, err := ancestry(s.Directory, s.owner)
	if err != nil {
		return Store{}, err
	}
	f, err := openFile(s.formatPath(), s.owner, false)
	if err != nil {
		return Store{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Store{}, err
	}
	raw, err := io.ReadAll(io.LimitReader(f, 2049))
	if err != nil {
		return Store{}, err
	}
	parts := bytes.Split(raw, []byte{'\n'})
	if len(raw) > 2048 || len(parts) != 3 || len(parts[2]) != 0 {
		return Store{}, ErrAuthority
	}
	var intent formatIntent
	var ready formatReady
	if json.Unmarshal(parts[0], &intent) != nil || json.Unmarshal(parts[1], &ready) != nil {
		return Store{}, ErrAuthority
	}
	canonicalIntent, _ := json.Marshal(intent)
	canonicalReady, _ := json.Marshal(ready)
	nonce, err := hex.DecodeString(intent.Nonce)
	if err != nil || len(nonce) != 32 || hex.EncodeToString(nonce) != intent.Nonce || intent.Version != formatVersion || intent.Ledger != s.Directory || !bytes.Equal(parts[0], canonicalIntent) || !bytes.Equal(parts[1], canonicalReady) {
		return Store{}, ErrAuthority
	}
	li, err := directory(s.Directory, s.owner)
	if err != nil {
		return Store{}, err
	}
	st := li.Sys().(*syscall.Stat_t)
	if ready.Device != uint64(st.Dev) || ready.Inode != uint64(st.Ino) {
		return Store{}, ErrAuthority
	}
	if err = s.checkFormatObjects(ready); err != nil {
		return Store{}, err
	}
	original := s.Check
	identity := s.Identity
	s.Check = func(i quotaoperation.Identity) error {
		if i != identity {
			return ErrAuthority
		}
		if err := ancestors(); err != nil {
			return err
		}
		now, err := directory(filepath.Dir(s.Directory), s.owner)
		if err != nil || !os.SameFile(parent, now) {
			return ErrAuthority
		}
		ledger, err := directory(s.Directory, s.owner)
		if err != nil || !os.SameFile(li, ledger) {
			return ErrAuthority
		}
		anchor, err := openFile(s.formatPath(), s.owner, false)
		if err != nil {
			return err
		}
		defer anchor.Close()
		named, err := anchor.Stat()
		if err != nil || !os.SameFile(fi, named) {
			return ErrAuthority
		}
		current, err := io.ReadAll(io.LimitReader(anchor, 2049))
		if err != nil {
			return err
		}
		if !bytes.Equal(current, raw) {
			return ErrAuthority
		}
		if err := s.checkFormatObjects(ready); err != nil {
			return err
		}
		return original(i)
	}
	if err = s.Check(s.Identity); err != nil {
		return Store{}, err
	}
	if err = s.withinExisting(ctx, func(*Journal) error { return nil }); err != nil {
		return Store{}, err
	}
	s.anchored = true
	s.anchoredDirectory = s.Directory
	s.anchoredCheck = s.Check
	return s, nil
}

func (s Store) checkFormatObjects(ready formatReady) error {
	for path, expected := range map[string][2]uint64{
		filepath.Join(s.Directory, "operations.lock"):                        {ready.LockDevice, ready.LockInode},
		filepath.Join(s.Directory, "operations", "indexed-v2", "journal.db"): {ready.DatabaseDevice, ready.DatabaseInode},
	} {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !private(info, s.owner, false) {
			return ErrAuthority
		}
		st := info.Sys().(*syscall.Stat_t)
		if uint64(st.Dev) != expected[0] || uint64(st.Ino) != expected[1] {
			return ErrAuthority
		}
	}
	return nil
}
