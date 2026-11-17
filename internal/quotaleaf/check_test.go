package quotaleaf

import "testing"

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
