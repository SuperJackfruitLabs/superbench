package quotaleaf

import (
	"errors"
	"testing"
)

func TestCheckRequiresProjectAndDirectoryInheritance(t *testing.T) {
	for _, c := range []struct {
		a         Attr
		project   uint32
		directory bool
		want      bool
	}{
		{Attr{ProjectID: 10001, Xflags: FlagProjInherit}, 10001, true, true},
		{Attr{ProjectID: 10001}, 10001, false, true},
		{Attr{ProjectID: 0, Xflags: FlagProjInherit}, 10001, true, false},      // chproj 0
		{Attr{ProjectID: 10002, Xflags: FlagProjInherit}, 10001, false, false}, // another bench
		{Attr{ProjectID: 10001}, 10001, true, false},                           // chattr -P
		{Attr{ProjectID: 0}, 0, false, false},                                  // no project at all
	} {
		if got := Check(c.a, c.project, c.directory); got != c.want {
			t.Errorf("%+v project %d directory %v: %v", c.a, c.project, c.directory, got)
		}
	}
}

// Hard links add names, not inodes: many names for one node count once
// against the inode cap, and the name bound is a separate error.
func TestBudgetCountsDistinctInodesAndNamesSeparately(t *testing.T) {
	b := newBudget(3, 100)
	for i := 0; i < 50; i++ { // one file reached by 50 hard links
		if err := b.inode(1, 42); err != nil {
			t.Fatalf("hard link %d counted as an inode: %v", i, err)
		}
	}
	if b.inode(1, 43) != nil || b.inode(2, 42) != nil {
		t.Fatal("distinct inodes under the cap refused")
	}
	if err := b.inode(1, 44); !errors.Is(err, ErrTreeLimit) {
		t.Fatalf("a fourth inode under a cap of three: %v", err)
	}
	if b.name(100) != nil {
		t.Fatal("names within the bound refused")
	}
	if err := b.name(1); !errors.Is(err, ErrTreeNames) || errors.Is(err, ErrTreeLimit) {
		t.Fatalf("names past the bound: %v", err)
	}
}
