package quotahelper

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestReclaimResultBindsTheTombstone(t *testing.T) {
	data := "/srv/data"
	good := ReclaimResult{ReclaimVersion, "card", Assignment{Path: filepath.Join(data, "benches", "card"), ProjectID: 10007}}
	raw := []byte(`{"Version":"` + ReclaimVersion + `","Key":"card","Allocation":{"Path":"/srv/data/benches/card","ProjectID":10007,"Bytes":0,"Inodes":0}}`)
	if got, err := ParseReclaimResult(raw, data, "card", 10007); err != nil || got != good {
		t.Fatalf("result: %+v %v", got, err)
	}
	for name, change := range map[string]func(*ReclaimResult){
		"version": func(r *ReclaimResult) { r.Version = "v0" },
		"key":     func(r *ReclaimResult) { r.Key = "other" },
		"path":    func(r *ReclaimResult) { r.Allocation.Path = "/srv/data/benches/other" },
		"project": func(r *ReclaimResult) { r.Allocation.ProjectID = 10008 },
		"bytes":   func(r *ReclaimResult) { r.Allocation.Bytes = 1024 },
		"inodes":  func(r *ReclaimResult) { r.Allocation.Inodes = 1 },
	} {
		r := good
		change(&r)
		if !errors.Is(r.Validate(data, "card", 10007), errReclaim) {
			t.Errorf("%s result accepted", name)
		}
	}
	if _, err := ParseReclaimResult(append(raw, []byte(` `)...), data, "card", 10007); err != nil {
		t.Fatal("trailing space is compact-equal", err)
	}
	if _, err := ParseReclaimResult([]byte(`{"Key":"card","Version":"`+ReclaimVersion+`","Allocation":{"Path":"/srv/data/benches/card","ProjectID":10007,"Bytes":0,"Inodes":0}}`), data, "card", 10007); err == nil {
		t.Fatal("reordered result accepted")
	}
	if !errors.Is(good.Validate(data, "card", 9999), errReclaim) {
		t.Fatal("a shared project accepted")
	}
}
