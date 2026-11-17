package scalarapi

import (
	"net/url"
	"strings"
)

// DisplayRepository is the repository a v1 response shows: no password, query
// or fragment.
func DisplayRepository(repository string) string {
	if !strings.Contains(repository, "://") {
		return repository
	}
	u, e := url.Parse(repository)
	if e != nil {
		return "[repository unavailable]"
	}
	if u.User != nil {
		u.User = url.User(u.User.Username())
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
