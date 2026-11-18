package projectscan

import (
	"errors"
	"fmt"
	"syscall"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaleaf"
)

func TestBenchesListsOnlyManagedBenchesAndMarksTombstones(t *testing.T) {
	raw := []byte(`{"Root":"/srv/d","Next":10003,"Fresh":{"bench:b":10001},"Assignments":{
"store":{"Path":"/srv/d/store","ProjectID":100,"Bytes":1,"Inodes":1},
"bench:b":{"Path":"/srv/d/benches/b","ProjectID":10001,"Bytes":1024,"Inodes":10},
"bench:a":{"Path":"/srv/d/benches/a","ProjectID":10000,"Bytes":1024,"Inodes":10},
"bench:gone":{"Path":"/srv/d/benches/gone","ProjectID":10002,"Bytes":0,"Inodes":0}}}`)
	got, err := benches("/srv/d", raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].key != "a" || got[1].key != "b" || !got[1].fresh || got[0].fresh || !got[2].retired || got[1].inodes != 10 {
		t.Fatalf("%+v", got)
	}
	for name, bad := range map[string]string{
		"outside root": `{"Assignments":{"bench:a":{"Path":"/etc/a","ProjectID":10000,"Bytes":1,"Inodes":1}}}`,
		"no project":   `{"Assignments":{"bench:a":{"Path":"/srv/d/benches/a","ProjectID":0,"Bytes":1,"Inodes":1}}}`,
		"bad key":      `{"Assignments":{"bench:../x":{"Path":"/srv/d/benches/../x","ProjectID":10000,"Bytes":1,"Inodes":1}}}`,
	} {
		if _, err := benches("/srv/d", []byte(bad)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// Only the walk's own escape errors are "outside"; a tree that changed under
// the walk, or the name bound, is "unverified".
func TestClassifyKeepsOutsideForRealEscapes(t *testing.T) {
	for err, want := range map[error]string{
		nil:                      "contained",
		quotaleaf.ErrLeafProject: "outside",
		fmt.Errorf("x: %w", quotaleaf.ErrTreeProject): "outside",
		quotaleaf.ErrTreeLimit:                        "outside",
		quotaleaf.ErrTreeNames:                        "unverified",
		syscall.ENOENT:                                "unverified",
		syscall.EACCES:                                "unverified",
		errors.New("io"):                              "unverified",
	} {
		if got := classify(err); got != want {
			t.Errorf("%v: %s, want %s", err, got, want)
		}
	}
}
