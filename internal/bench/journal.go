package bench

import (
	createfiles "forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/creation"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
)

type creation = createfiles.Intent

func (s Store) creationFiles() createfiles.Files {
	return createfiles.Files{State: s.State, Data: s.Data, Git: s.git, Write: atomicWrite}
}
func (s Store) journalPath(key string) string { return s.creationFiles().JournalPath(key) }
func (s Store) pending(key string, o Options, recovering bool) (*creation, error) {
	return s.creationFiles().Pending(key, o, recovering)
}
func (s Store) record(c *creation) error                 { return s.creationFiles().Record(c) }
func (s Store) finishCreate(c *creation) (*Bench, error) { return s.creationFiles().Finish(c) }
func (s Store) mirrorPath(repo string) string            { return repository.Path(s.Data, repo) }
