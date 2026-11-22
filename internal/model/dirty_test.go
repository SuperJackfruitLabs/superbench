package model

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestDirtyStatusNamesPathsFromPorcelain(t *testing.T) {
	// The daemon's Git trims output, so the first entry may lose its leading space.
	out := "M file\n?? src/__pycache__/a.pyc\nR  old -> new\n D gone\n?? \"tab\\there\"\nA  six\n?? seven"
	err := DirtyStatus("api", out)
	var dirty *DirtyError
	if !errors.Is(err, ErrDirty) || !errors.As(err, &dirty) {
		t.Fatal(err)
	}
	want := []string{"api/file", "api/src/__pycache__/a.pyc", "api/new", "api/gone", `api/"tab\there"`}
	if !reflect.DeepEqual(dirty.Paths, want) || dirty.More != 2 {
		t.Fatal(dirty.Paths, dirty.More)
	}
	if DirtyStatus("", "") != nil || DirtyStatus("", "\n") != nil {
		t.Fatal("clean status refused")
	}
	if err := DirtyStatus("", "?"); err != ErrDirty {
		t.Fatal("unparsed status lost its refusal", err)
	}
}

func TestDirtyMessageStaysWithinBound(t *testing.T) {
	base := "workspace has uncommitted or untracked files; commit or remove them before checkpoint or release"
	e := Dirty([]string{"a", "b"}).(*DirtyError)
	if got := e.Message(base, 256); got != base+": a, b" {
		t.Fatal(got)
	}
	long := strings.Repeat("x", 120)
	e = Dirty([]string{"a", long, "c", "d", "e", "f", "g"}).(*DirtyError)
	if got := e.Message(base, 256); got != base+": a, "+long+", c, d, e and 2 more" || len(got) > 256 {
		t.Fatal(len(got), got)
	}
	e = Dirty([]string{long, long, "c"}).(*DirtyError)
	if got := e.Message(base, 256); got != base+": "+long+" and 2 more" {
		t.Fatal(got)
	}
	e = Dirty([]string{strings.Repeat("é", 200), "b"}).(*DirtyError)
	got := e.Message(base, 256)
	if len(got) > 256 || !strings.HasSuffix(got, "... and 1 more") || !strings.HasPrefix(got, base+": é") || !strings.ContainsRune(got[len(base):], 'é') {
		t.Fatal(len(got), got)
	}
	if strings.ContainsRune(got, '�') {
		t.Fatal("shortened path split a character", got)
	}
	if Dirty(nil) != ErrDirty || Dirty([]string{"\n"}).(*DirtyError).Paths[0] != `"\n"` {
		t.Fatal("empty or unprintable names")
	}
}
