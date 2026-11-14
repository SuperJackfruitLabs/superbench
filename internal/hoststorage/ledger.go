package hoststorage

import "errors"

// Ledger v2 omits the legacy VolumeID field. Earlier helpers fail closed on
// that absent identity instead of interpreting a smaller allocation as default.
type policyBinding struct {
	OwnerUID, OwnerGID                                    int
	BenchBytes, StoreBytes, MirrorBytes, ReserveBytes     uint64
	BenchInodes, StoreInodes, MirrorInodes, ReserveInodes uint64
}

func (p Policy) binding() policyBinding {
	return policyBinding{p.OwnerUID, p.OwnerGID, p.BenchBytes, p.StoreBytes, p.MirrorBytes, p.ReserveBytes, p.BenchInodes, p.StoreInodes, p.MirrorInodes, p.ReserveInodes}
}

func (l ledger) filesystem() string {
	if l.FormatVersion == 2 {
		return l.FilesystemUUID
	}
	return l.VolumeID
}

func (p Policy) ledgerHeader(l ledger) error {
	switch l.FormatVersion {
	case 0:
		if l.VolumeID == "" || l.FilesystemUUID != "" || l.Binding != nil {
			return errors.New("invalid legacy ledger identity")
		}
	case 2:
		if l.VolumeID != "" || !volumePattern.MatchString(l.FilesystemUUID) || l.Binding == nil {
			return errors.New("invalid quota ledger identity or policy binding")
		}
		actual, expected := *l.Binding, p.binding()
		if p.upgradingInodes {
			actual.BenchInodes, actual.StoreInodes, actual.MirrorInodes, actual.ReserveInodes = 0, 0, 0, 0
			expected.BenchInodes, expected.StoreInodes, expected.MirrorInodes, expected.ReserveInodes = 0, 0, 0, 0
		}
		if actual != expected {
			return errors.New("quota policy differs from the pinned ledger; explicit migration required")
		}
	default:
		return errors.New("unsupported quota ledger format")
	}
	return nil
}

func (p Policy) bindLedger(l *ledger) {
	identity := l.filesystem()
	l.FormatVersion, l.FilesystemUUID, l.VolumeID = 2, identity, ""
	binding := p.binding()
	l.Binding = &binding
}

func (p Policy) request(kind string, size uint64) error {
	if size != 0 && (kind != "bench" || size < 1024 || size%1024 != 0 || size > p.BenchBytes) {
		return errors.New("requested bench quota must be whole KiB within the root policy ceiling")
	}
	return nil
}
