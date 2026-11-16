package storageproof

import (
	"bytes"
	"encoding/json"
	"errors"
)

const AllocationVersion = "superbench.allocation-observation.v1"

// AllocationObservation may prove a selected logical allocation absent from an
// otherwise fully validated existing managed ledger. It is not an allocated
// StorageIdentity and grants no allocation, adoption or pathname authority.
// An unmanaged observation binds the physical root without inventing a ledger.
type AllocationObservation Identity

func (o AllocationObservation) Allocated() bool { return o.Managed && o.Assignment.ProjectID != 0 }

func (o AllocationObservation) Validate() error {
	if o.Version != AllocationVersion {
		return errors.New("invalid allocation observation version")
	}
	s := Identity(o)
	s.Version = Version
	return s.validate(true)
}

func (o AllocationObservation) Bytes() ([]byte, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(o)
	if len(raw) > MaxIdentity {
		return nil, errors.New("allocation observation exceeds limit")
	}
	return raw, err
}

func ParseAllocationObservation(raw []byte) (AllocationObservation, error) {
	var o AllocationObservation
	if len(raw) > MaxIdentity {
		return o, errors.New("allocation observation exceeds limit")
	}
	if err := StrictJSON(raw, &o); err != nil {
		return AllocationObservation{}, err
	}
	canonical, err := o.Bytes()
	if err != nil {
		return AllocationObservation{}, err
	}
	if !bytes.Equal(raw, canonical) {
		return AllocationObservation{}, errors.New("allocation observation is not canonical")
	}
	return o, nil
}
