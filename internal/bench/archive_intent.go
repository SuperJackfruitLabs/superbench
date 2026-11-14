package bench

import (
	"errors"
	"path/filepath"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
)

func (s Store) freezeJournal(key string) string {
	return filepath.Join(s.State, "archive-freeze-"+key+".json")
}

func (s Store) saveLifecycleIntent(path string, raw []byte) error {
	if len(raw) > reap.MaxManifest {
		return errors.New("lifecycle intent exceeds limit")
	}
	space, e := s.space(s.State)
	if e != nil {
		return e
	}
	if space.Free < uint64(len(raw))*2+(1<<20) {
		return errors.New("insufficient state space for durable lifecycle intent")
	}
	return atomicWrite(s.State, path, raw)
}
