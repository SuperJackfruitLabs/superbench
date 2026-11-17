//go:build linux

package payloadbootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperationjournal"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/serviceidentity"
)

// Manager has no caller-supplied filesystem or Git authority. Open reads both
// installed policies and binds an actual existing indexed operation barrier.
// The zero value cannot bootstrap or inspect. Commands use the fixed helper;
// there is no service route that enables fresh payload materialization.
type Manager struct {
	policy          payloadexec.Policy
	storage         hoststorage.Policy
	registry        serviceidentity.HelperRegistry
	operation       quotaoperation.Identity
	store           *quotaoperationjournal.Store
	digest          string
	maintenanceGate func(context.Context, hoststorage.Policy) error
	reclaim         bool // set only on Reclaim's own copy; see reclaimable
}

func Open(ctx context.Context, control *quotaoperationjournal.Control, i quotaoperation.Identity) (*Manager, error) {
	if os.Getuid() != 0 || os.Geteuid() != 0 || !i.Valid() || control == nil {
		return nil, ErrBootstrap
	}
	registry, err := serviceidentity.ResolveHelperRegistry()
	if err != nil {
		return nil, errors.Join(ErrBootstrap, err)
	}
	public, err := registry.Policy()
	if err != nil {
		return nil, err
	}
	private, err := payloadexec.LoadFixedPolicy()
	if err != nil || private != public {
		return nil, errors.Join(ErrBootstrap, err)
	}
	storage, err := hoststorage.Load("/etc/superbench/storage.json")
	if err != nil {
		return nil, err
	}
	if storage.Root != private.DataRoot || storage.OwnerUID != int(private.DaemonUID) || storage.OwnerGID != int(private.DaemonGID) {
		return nil, ErrBootstrap
	}
	m := &Manager{policy: private, storage: storage, registry: registry, operation: i, maintenanceGate: quotaoperationcontrol.CheckMaintenance}
	raw, err := private.Bytes()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	m.digest = hex.EncodeToString(sum[:])
	m.store, err = control.Operation(ctx, i, func(current quotaoperation.Identity) error {
		if err := m.check(); err != nil {
			return err
		}
		authority, err := storage.OperationAuthority(ctx)
		if err != nil {
			return err
		}
		binary, err := quotahelper.SelfBinarySHA256()
		if err != nil {
			return err
		}
		if current != i || authority != current.Authority || binary != current.BinarySHA256 {
			return quotaoperation.ErrAuthority
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return m, nil
}
func (m *Manager) check() error {
	if m == nil || !m.operation.Valid() || m.digest == "" || os.Getuid() != 0 || os.Geteuid() != 0 {
		return ErrBootstrap
	}
	if err := m.registry.Recheck(); err != nil {
		return err
	}
	p, err := payloadexec.LoadFixedPolicy()
	if err != nil || p != m.policy {
		return errors.Join(ErrBootstrap, err)
	}
	current, err := hoststorage.Load("/etc/superbench/storage.json")
	if err != nil || !reflect.DeepEqual(current, m.storage) {
		return errors.Join(ErrBootstrap, err)
	}
	return nil
}
func (m *Manager) root(s Spec) string { return filepath.Join(m.policy.DataRoot, "benches", s.Key) }
func (m *Manager) intentPath(s Spec) string {
	return filepath.Join(m.storage.Ledger, "payload-bootstrap", s.Key+".json")
}
