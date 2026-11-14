//go:build !linux

package hoststorage

import "errors"

func (Policy) Apply(string, string) (Assignment, error) {
	return Assignment{}, errors.New("XFS quota management requires Linux")
}

func (Policy) Release(string, uint32) (Assignment, error) {
	return Assignment{}, errors.New("XFS quota management requires Linux")
}

func (Policy) MigrateInodes() ([]Assignment, error) {
	return nil, errors.New("XFS quota management requires Linux")
}
