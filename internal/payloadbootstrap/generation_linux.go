//go:build linux

package payloadbootstrap

import (
	"bytes"
	"encoding/json"
	"path/filepath"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshgeneration"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storagecustody"
)

// predecessor is the reclaimed generation a later one follows, from its own
// retained intent, read and never rewritten (freshgeneration.Follows). Its
// recorded allocation is the tombstone the ledger must hold for the key.
func (m *Manager) predecessor(s Spec) (hoststorage.Assignment, error) {
	var i intent
	raw, err := storagecustody.ReadProtected(filepath.Join(m.storage.Ledger, "payload-bootstrap", freshgeneration.Stem(s.Key, freshgeneration.Before(s.Generation))+".json"), maxIntent)
	if err != nil {
		return hoststorage.Assignment{}, err
	}
	err = json.Unmarshal(raw, &i)
	canonical, _ := json.Marshal(i)
	prior := freshgeneration.Prior{Key: i.Spec.Key, ID: i.Spec.ID, Generation: i.Spec.Generation, Phase: i.Phase, Authority: i.Operation.Authority, Path: i.Allocation.Path, ProjectID: i.Allocation.ProjectID}
	if err != nil || !bytes.Equal(raw, canonical) || i.Spec.Validate() != nil || freshgeneration.Follows(prior, s.Key, s.Previous, s.Generation, m.operation.Authority, m.root(s)) != nil {
		return hoststorage.Assignment{}, ErrBootstrap
	}
	return i.Allocation, nil
}
