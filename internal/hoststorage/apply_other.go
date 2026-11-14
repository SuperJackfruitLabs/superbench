//go:build !linux

package hoststorage

import "errors"

func (Policy) Apply(string, string) (Assignment, error) {
	return Assignment{}, errors.New("XFS quota management requires Linux")
}
