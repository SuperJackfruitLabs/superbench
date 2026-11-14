package bench

import (
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/output"
)

func (s Store) logLimits() (per, total uint64, files int, e error) {
	per, total, files = 1<<20, 64<<20, 4096
	config, e := s.hostConfig()
	if e != nil {
		return 0, 0, 0, e
	}
	if config != nil {
		if config.MaxLogBytes != 0 {
			per = config.MaxLogBytes
		}
		if config.MaxLogsBytes != 0 {
			total = config.MaxLogsBytes
		}
		if config.MaxLogFiles != 0 {
			files = config.MaxLogFiles
		}
	}
	return
}
func (s Store) reserveLog(db database) (uint64, error) {
	per, total, maxFiles, e := s.logLimits()
	if e != nil {
		return 0, e
	}
	var reservations []output.Reservation
	for _, b := range db {
		if b.Lease != nil && b.Lease.Execution != nil {
			e := b.Lease.Execution
			reservations = append(reservations, output.Reservation{Log: e.Log, Limit: e.LogLimitBytes})
		}
	}
	outstanding, e := output.Reserve(s.State, per, total, maxFiles, reservations)
	if e != nil {
		return 0, e
	}
	status, e := s.HostStatus()
	if e != nil {
		return 0, e
	}
	floor := uint64(1 << 20)
	if status.Config != nil {
		floor = status.Config.MinStateFreeBytes
	}
	if status.State.Free < floor || per+outstanding > status.State.Free-floor {
		return 0, errors.New("state free-space reserve cannot cover outstanding log allocations")
	}
	return per, nil
}
