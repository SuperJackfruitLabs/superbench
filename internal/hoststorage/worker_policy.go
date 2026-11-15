package hoststorage

import (
	"errors"
	"fmt"
	"path/filepath"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/output"
)

func (s Worker) Admit() error {
	status, e := s.Status()
	if e != nil {
		return e
	}
	minData, minState := uint64(64<<20), uint64(1<<20)
	if status.Config != nil {
		minData = status.Config.MinDataFreeBytes
		minState = status.Config.MinStateFreeBytes
		if status.Data.Device == status.State.Device {
			return errors.New("managed state must be outside the quota data filesystem")
		}
	}
	if status.Data.Free < minData {
		return fmt.Errorf("data free-space reserve reached: %d available, %d required", status.Data.Free, minData)
	}
	if status.State.Free < minState {
		return fmt.Errorf("state free-space reserve reached: %d available, %d required", status.State.Free, minState)
	}
	if status.Config != nil {
		for _, key := range []string{"store", "mirrors"} {
			if _, e = s.Quota(*status.Config, "shared", key, 0); e != nil {
				return e
			}
		}
	}
	return nil
}

// Configure applies host policy while the caller holds its state mutation lock.
// Existing records are never silently adopted into a new managed host policy.
func (s Worker) Configure(c HostConfig, existingRecords int) error {
	c = c.Defaults()
	if err := c.Validate(s.Data); err != nil {
		return err
	}
	prior, e := s.Config()
	if e != nil {
		return e
	}
	if prior != nil && *prior != c {
		return errors.New("host is already configured; explicit migration required")
	}
	if prior == nil && existingRecords != 0 {
		return errors.New("managed host requires fresh state; existing benches are not silently adopted")
	}
	data, e := s.Space(s.Data)
	if e != nil {
		return e
	}
	state, e := s.Space(s.State)
	if e != nil {
		return e
	}
	if data.Device == state.Device {
		return errors.New("managed state must be outside the quota data filesystem")
	}
	if data.Free < c.MinDataFreeBytes || state.Free < c.MinStateFreeBytes {
		return errors.New("insufficient host reserve for configuration")
	}
	for _, key := range []string{"store", "mirrors"} {
		if _, e = s.Quota(c, "shared", key, 0); e != nil {
			return e
		}
	}
	return save(s.State, filepath.Join(s.State, "host.json"), c)
}
func (s Worker) LogLimits() (per, total uint64, files int, e error) {
	per, total, files = 1<<20, 64<<20, 4096
	config, e := s.Config()
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
func (s Worker) ReserveLog(db map[string]*model.Bench) (uint64, error) {
	per, total, maxFiles, e := s.LogLimits()
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
	status, e := s.Status()
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
