package model

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxDirtyPaths is how many offending paths a dirty refusal names; the rest
// are counted.
const MaxDirtyPaths = 5

// DirtyError is ErrDirty naming the first offending paths, relative to the
// workspace root (or the bench's checkout), so the holder knows what to
// commit or remove. It carries names only, never file contents.
type DirtyError struct {
	Paths []string // at most MaxDirtyPaths
	More  int      // offending paths not named
}

func (e *DirtyError) Error() string { return ErrDirty.Error() + ": " + e.list(len(e.Paths)) }
func (e *DirtyError) Unwrap() error { return ErrDirty }

// Message appends the named paths to a client's static refusal while it
// stays within limit bytes. Paths that do not fit are counted instead, and a
// first path too long to fit is shortened rather than dropped.
func (e *DirtyError) Message(base string, limit int) string {
	if e == nil || len(e.Paths) == 0 || len(base)+2 > limit {
		return base
	}
	for n := len(e.Paths); n > 0; n-- {
		if m := base + ": " + e.list(n); len(m) <= limit {
			return m
		}
	}
	tail := e.tail(len(e.Paths) - 1)
	room := limit - len(base) - len(": ") - len(tail) - len("...")
	if room <= 0 {
		return base
	}
	first := e.Paths[0]
	for len(first) > room {
		_, size := utf8.DecodeLastRuneInString(first)
		first = first[:len(first)-size]
	}
	return base + ": " + first + "..." + tail
}

func (e *DirtyError) list(n int) string {
	return strings.Join(e.Paths[:n], ", ") + e.tail(len(e.Paths)-n)
}

func (e *DirtyError) tail(unnamed int) string {
	if more := unnamed + e.More; more > 0 {
		return " and " + strconv.Itoa(more) + " more"
	}
	return ""
}

// DirtyStatus returns nil for empty `git status --porcelain` (v1) output, and
// otherwise a DirtyError naming its paths. The output may have been trimmed,
// losing the first entry's leading space. Git quotes unusual names, so each
// path is one printable line; a rename names its destination. A non-empty
// prefix (a member name) is joined to each path.
func DirtyStatus(prefix, porcelain string) error {
	if strings.TrimSpace(porcelain) == "" {
		return nil
	}
	var paths []string
	for _, line := range strings.Split(porcelain, "\n") {
		line = strings.TrimRight(line, "\r")
		var path string
		switch {
		case len(line) > 3 && line[2] == ' ':
			path = line[3:]
		case len(line) > 2 && line[1] == ' ': // first entry with its leading space trimmed
			path = line[2:]
		default:
			continue
		}
		if _, to, ok := strings.Cut(path, " -> "); ok {
			path = to
		}
		if prefix != "" {
			path = prefix + "/" + path
		}
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return ErrDirty
	}
	return Dirty(paths)
}

// DirtyMember is DirtyStatus for one member's checkout, naming the member only
// when the workspace has several: each checkout is the root's entry of that name.
func DirtyMember(b *Bench, member, porcelain string) error {
	if len(b.Workspace.Members) > 1 {
		return DirtyStatus(member, porcelain)
	}
	return DirtyStatus("", porcelain)
}

// Dirty returns a DirtyError naming the first MaxDirtyPaths of paths and
// counting the rest. Unprintable names are quoted.
func Dirty(paths []string) error {
	e := &DirtyError{}
	for _, path := range paths {
		if len(e.Paths) == MaxDirtyPaths {
			e.More++
			continue
		}
		if strings.IndexFunc(path, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 || !utf8.ValidString(path) {
			path = strconv.Quote(path)
		}
		e.Paths = append(e.Paths, path)
	}
	if len(e.Paths) == 0 {
		return ErrDirty
	}
	return e
}
