package cleanworker

import (
	"bytes"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
)

func tree() reap.Tree {
	return reap.Tree{Root: reap.Entry{Path: ".", Mode: uint32(os.ModeDir | 0700), Device: 8, Inode: 10},
		Entries: []reap.Entry{{Path: "a", Mode: 0600, Device: 8, Inode: 11, SHA256: strings.Repeat("a", 64)}}}
}

func observed() Reply {
	t := tree()
	return Reply{Version: Version, Tree: &t, Owners: []Owner{
		{Path: ".", Device: 8, Inode: 10, Nlink: 2, Mode: uint32(os.ModeDir | 0700), UID: 7, GID: 7},
		{Path: "a", Device: 8, Inode: 11, Nlink: 1, Mode: 0600, UID: 7, GID: 7}}}
}

func TestFramesRoundTripOnlyInTheirCanonicalForm(t *testing.T) {
	approved := tree()
	for _, r := range []Request{{Version, Observe, "/srv/data/benches/card", nil}, {Version, Delete, "/srv/data/benches/card", &approved}} {
		raw, err := EncodeRequest(r)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeRequest(raw)
		if err != nil || !reflect.DeepEqual(got, r) {
			t.Fatalf("request round trip: %+v %v", got, err)
		}
		bad := map[string][]byte{
			"unknown":    bytes.Replace(raw, []byte(`{"Version"`), []byte(`{"Extra":1,"Version"`), 1),
			"duplicate":  bytes.Replace(raw, []byte(`{"Version":"`+Version+`"`), []byte(`{"Version":"`+Version+`","Version":"`+Version+`"`), 1),
			"indented":   bytes.Replace(raw, []byte(`,"Mode"`), []byte(`, "Mode"`), 1),
			"trailing":   append(append([]byte{}, raw...), '\n'),
			"lowercase":  bytes.Replace(raw, []byte(`"Mode"`), []byte(`"mode"`), 1),
			"oversize":   append(append([]byte{}, raw...), bytes.Repeat([]byte(" "), MaxFrame)...),
			"not-utf8":   bytes.Replace(raw, []byte("card"), []byte("c\xffrd"), 1),
			"truncated":  raw[:len(raw)-1],
			"other-mode": bytes.Replace(raw, []byte(`"Mode":"`+r.Mode), []byte(`"Mode":"remove`), 1),
		}
		for name, frame := range bad {
			if got, err := DecodeRequest(frame); !errors.Is(err, ErrWorker) || !reflect.DeepEqual(got, Request{}) {
				t.Errorf("%s %s request accepted: %v", r.Mode, name, err)
			}
		}
	}
	for name, r := range map[string]Request{
		"version":          {"v0", Observe, "/srv/data/benches/card", nil},
		"relative":         {Version, Observe, "srv/data", nil},
		"unclean":          {Version, Observe, "/srv/data/../etc", nil},
		"filesystem-root":  {Version, Observe, "/", nil},
		"nul":              {Version, Observe, "/srv/\x00", nil},
		"observe-approved": {Version, Observe, "/srv/data/benches/card", &approved},
		"delete-no-tree":   {Version, Delete, "/srv/data/benches/card", nil},
		"no-mode":          {Version, "", "/srv/data/benches/card", nil},
	} {
		if _, err := EncodeRequest(r); !errors.Is(err, ErrWorker) {
			t.Errorf("%s request encoded: %v", name, err)
		}
	}
	raw, err := EncodeReply(observed(), Observe)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := DecodeReply(raw, Observe); err != nil || !reflect.DeepEqual(got, observed()) {
		t.Fatalf("reply round trip: %+v %v", got, err)
	}
	if _, err := DecodeReply(raw, Delete); !errors.Is(err, ErrWorker) {
		t.Error("observation accepted as a delete reply", err)
	}
	done, err := EncodeReply(Reply{Version: Version}, Delete)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReply(done, Observe); !errors.Is(err, ErrWorker) {
		t.Error("an empty delete reply accepted as an observation", err)
	}
}

func TestObservationRepliesNameEveryEntryOnce(t *testing.T) {
	for name, mutate := range map[string]func(*Reply){
		"missing-owner": func(r *Reply) { r.Owners = r.Owners[:1] },
		"extra-owner":   func(r *Reply) { r.Owners = append(r.Owners, r.Owners[1]) },
		"other-path":    func(r *Reply) { r.Owners[1].Path = "b" },
		"other-inode":   func(r *Reply) { r.Owners[1].Inode++ },
		"other-device":  func(r *Reply) { r.Owners[0].Device++ },
		"other-mode":    func(r *Reply) { r.Owners[1].Mode = uint32(os.ModeSymlink | 0777) },
		"no-tree":       func(r *Reply) { r.Tree = nil },
		"version":       func(r *Reply) { r.Version = "v0" },
	} {
		r := observed()
		mutate(&r)
		if _, err := EncodeReply(r, Observe); !errors.Is(err, ErrWorker) {
			t.Errorf("%s reply accepted: %v", name, err)
		}
	}
}
