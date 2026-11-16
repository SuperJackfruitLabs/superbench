package hoststorage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storagecustody"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storageproof"
)

const MaxStorageIdentity = storageproof.MaxIdentity
const storageVersion = storageproof.Version

type StorageIdentity = storageproof.Identity

func ParseStorageIdentity(raw []byte) (StorageIdentity, error) {
	return storageproof.ParseIdentity(raw)
}
func strictStorageJSON(raw []byte, v any) error { return storageproof.StrictJSON(raw, v) }
func storageDigest(raw []byte) string           { return storageproof.Digest(raw) }

func physicalStorageRoot(root string) (uint64, uint64, error) {
	return storagecustody.PhysicalRoot(root)
}

// InspectStorage never allocates, creates a missing root, repairs an anchor or
// falls back to an existing ancestor. expected comes from the durable bench row.
func (w Worker) InspectStorage(ctx context.Context, key string, expected Assignment) (StorageIdentity, error) {
	return w.inspectStorage(ctx, key, expected, storageCommand)
}

func (w Worker) inspectStorage(ctx context.Context, key string, expected Assignment, command func(context.Context, string, ...string) ([]byte, error)) (StorageIdentity, error) {
	return w.inspectStorageRoot(ctx, key, &expected, false, command)
}

func (w Worker) inspectStorageRoot(ctx context.Context, key string, expected *Assignment, allocation bool, command func(context.Context, string, ...string) ([]byte, error)) (StorageIdentity, error) {
	var zero StorageIdentity
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if !Key.MatchString(key) || expected != nil && expected.Path != filepath.Join(w.Data, "benches", key) {
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
	assignment := Assignment{Path: filepath.Join(w.Data, "benches", key)}
	if expected != nil {
		assignment = *expected
	}
	s := StorageIdentity{Version: storageVersion, Root: w.Data, BenchKey: key, Device: device, Inode: inode, Assignment: assignment}
	if c == nil {
		s.Mount, err = unmanagedStorageMount(w.Data)
	} else {
		args := []string{}
		if c.PolicyOverride != "" {
			args = append(args, "--policy", c.PolicyOverride)
		}
		verb := "verify"
		if allocation {
			verb = "inspect-allocation"
		}
		args = append(args, verb, "bench", key)
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
		if !s.Managed || s.Root != w.Data || s.BenchKey != key || s.Device != device || s.Inode != inode || expected != nil && s.Assignment != *expected || s.ConfigSHA256 != "" {
			return zero, errors.New("quota verifier returned different storage authority")
		}
		config, _ := json.Marshal(c)
		s.ConfigSHA256 = storageDigest(config)
	}
	if err != nil {
		return zero, err
	}
	if allocation {
		if c != nil && s.Version != storageproof.AllocationVersion {
			return zero, errors.New("quota verifier returned wrong allocation version")
		}
		s.Version = storageproof.AllocationVersion
		err = AllocationObservation(s).Validate()
	} else {
		err = s.Validate()
	}
	if err != nil {
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
	if err := s.Validate(); err != nil {
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

func storageCommand(ctx context.Context, binary string, args ...string) ([]byte, error) {
	return storageproof.VerificationCommand(ctx, binary, args...)
}
