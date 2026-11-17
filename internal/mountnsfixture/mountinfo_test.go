//go:build freshstoragefixture

package mountnsfixture

import (
	"errors"
	"testing"
)

const mountinfo = `22 1 8:1 / / rw,relatime shared:1 - ext4 /dev/sda1 rw
99 22 7:1 / /srv/superbench-data rw,relatime shared:42 - xfs /dev/loop1 rw,prjquota
120 22 7:7 / /srv/super\040bench rw - xfs /dev/loop7 rw
130 99 0:50 / /srv/superbench-data rw - tmpfs tmpfs rw
`

func TestParseMountIDReadsTheTopmostMountAtTheRoot(t *testing.T) {
	for root, want := range map[string]string{"/": "22", "/srv/superbench-data": "130", "/srv/super bench": "120"} {
		if got, err := ParseMountID([]byte(mountinfo), root); err != nil || got != want {
			t.Fatalf("%s: got %q %v, want %s", root, got, err, want)
		}
	}
}

func TestParseMountIDSeparatesAnAbsentMountFromBadInput(t *testing.T) {
	if _, err := ParseMountID([]byte(mountinfo), "/srv/other"); !errors.Is(err, ErrNotVisible) || errors.Is(err, ErrNamespace) {
		t.Fatalf("absent mount: %v", err)
	}
	for name, raw := range map[string]string{
		"short line":       "22 1 8:1 /\n",
		"non-numeric ID":   "x 1 8:1 / /srv/superbench-data rw - xfs d rw\n",
		"truncated escape": "22 1 8:1 / /srv\\04 rw - xfs d rw\n",
		"invalid escape":   "22 1 8:1 / /srv\\9zz rw - xfs d rw\n",
	} {
		_, err := ParseMountID([]byte(raw), "/srv/superbench-data")
		if err == nil || errors.Is(err, ErrNotVisible) {
			t.Fatalf("%s: malformed mountinfo was not refused as such: %v", name, err)
		}
	}
}
