package cleanworker

import (
	"errors"
	"testing"
)

func TestPurgeFramesNeedTheirTargetAndNothingElse(t *testing.T) {
	approved := tree()
	target := &Target{Device: 8, Inode: 10, ProjectID: 10000}
	r := Request{Version, Purge, "/srv/data/benches/card", nil, target}
	raw, err := EncodeRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := DecodeRequest(raw); err != nil || *got.Target != *target {
		t.Fatalf("purge round trip: %+v %v", got, err)
	}
	// Older modes keep their exact encoding: the target field is omitted.
	if raw, err := EncodeRequest(Request{Version, Observe, "/srv/x", nil, nil}); err != nil || string(raw) != `{"Version":"`+Version+`","Mode":"observe","Root":"/srv/x","Approved":null}` {
		t.Fatalf("observe encoding changed: %s %v", raw, err)
	}
	for name, r := range map[string]Request{
		"no-target":       {Version, Purge, "/srv/x", nil, nil},
		"approved":        {Version, Purge, "/srv/x", &approved, target},
		"zero-device":     {Version, Purge, "/srv/x", nil, &Target{0, 10, 10000}},
		"zero-inode":      {Version, Purge, "/srv/x", nil, &Target{8, 0, 10000}},
		"default-project": {Version, Purge, "/srv/x", nil, &Target{8, 10, 0}},
		"shared-project":  {Version, Purge, "/srv/x", nil, &Target{8, 10, 9999}},
		"observe-target":  {Version, Observe, "/srv/x", nil, target},
		"delete-target":   {Version, Delete, "/srv/x", &approved, target},
		"filesystem-root": {Version, Purge, "/", nil, target},
		"relative-root":   {Version, Purge, "srv/x", nil, target},
	} {
		if _, err := EncodeRequest(r); !errors.Is(err, ErrWorker) {
			t.Errorf("%s purge request encoded: %v", name, err)
		}
	}
	done, err := EncodeReply(Reply{Version: Version}, Purge)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReply(done, Purge); err != nil {
		t.Fatal(err)
	}
	if _, err := EncodeReply(observed(), Purge); !errors.Is(err, ErrWorker) {
		t.Error("a purge reply carried an observation", err)
	}
}

func TestMountsBeneathTheTreeRefuse(t *testing.T) {
	line := func(point string) string {
		return "36 35 98:0 / " + point + " rw,noatime master:1 - xfs /dev/loop0 rw\n"
	}
	root := "/srv/data/benches/card"
	ok := line("/") + line("/srv/data") + line("/srv/data/benches/card2") + line(`/srv/data/benches/card\040x`)
	if err := mountsBeneath([]byte(ok), root); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"at":        line(root),
		"beneath":   line(root + "/api/node_modules"),
		"escaped":   line(`/srv/data/benches/card/a\040b`),
		"short":     "36 35 98:0 /\n",
		"bad-octal": line(`/srv/data/benches/card\09`),
		"truncated": line(`/srv/data\04`),
	} {
		if err := mountsBeneath([]byte(ok+raw), root); !errors.Is(err, ErrWorker) {
			t.Errorf("%s mount accepted: %v", name, err)
		}
	}
	if got, ok := unescapeMount(`a\040b\134c\011`); !ok || got != "a b\\c\t" {
		t.Fatalf("unescape: %q %v", got, ok)
	}
}
