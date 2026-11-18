//go:build !linux

package projectscan

import "errors"

// Scan needs Linux XFS; elsewhere there is no managed storage to scan.
func Scan(string, string) ([]Result, error) {
	return nil, errors.New("project scan requires Linux managed storage")
}
