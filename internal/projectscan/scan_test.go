package projectscan

import "testing"

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
