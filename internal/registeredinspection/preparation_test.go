package registeredinspection

import (
	"bytes"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/registeredinspection/layout"
	"strings"
	"testing"
)

func TestPreparationRequestIsClosedAndScopeFree(t *testing.T) {
	r := PreparationRequest{PreparationVersion, "example", strings.Repeat("a", 32), strings.Repeat("b", 64), "web"}
	raw, err := r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParsePreparationRequest(raw)
	if err != nil || got != r {
		t.Fatalf("request roundtrip: %+v %v", got, err)
	}
	for _, field := range []string{`"Scope":{}`, `"path":"/checkout"`, `"UID":1001`, `"Arguments":[]`, `"Member":"web"`} {
		bad := append(append([]byte(nil), raw[:len(raw)-1]...), []byte(","+field+"}")...)
		if _, err := ParsePreparationRequest(bad); !errors.Is(err, ErrBoundary) {
			t.Fatalf("accepted extra or duplicate field %s: %v", field, err)
		}
	}
	for _, bad := range [][]byte{append([]byte{0xef, 0xbb, 0xbf}, raw...), bytes.Replace(raw, []byte(`"Member":"web"`), []byte(`"Member":null`), 1), bytes.Replace(raw, []byte(`"Member":"web"`), []byte(`"Member":"../web"`), 1)} {
		if _, err := ParsePreparationRequest(bad); !errors.Is(err, ErrBoundary) {
			t.Fatalf("invalid request accepted: %s %v", bad, err)
		}
	}
}
func TestPreparationResponseBindsCompleteLayoutAndFixedInputs(t *testing.T) {
	p := Policy{DaemonUID: 1001, DaemonGID: 1001, PayloadUID: 1002, PayloadGID: 1002, DataRoot: "/data", StateRoot: "/state", CgroupRoot: "/sys/fs/cgroup/payload"}
	digest, err := layout.PolicyDigest(p)
	if err != nil {
		t.Fatal(err)
	}
	l := Layout{Version: layout.LayoutVersion, Key: "example", ID: strings.Repeat("a", 32), PolicyDigest: digest, Root: Node{Device: 1, Inode: 1}, RootHome: Node{Device: 1, Inode: 2}, RootTemp: Node{Device: 1, Inode: 3}, Members: []layout.LayoutMember{{Name: "web", Checkout: Node{Device: 1, Inode: 4}, Admin: Node{Device: 1, Inode: 5}, Home: Node{Device: 1, Inode: 6}, Temp: Node{Device: 1, Inode: 7}}}}
	v := Preparation{Version: PreparationVersion, Layout: l, Inputs: absentInputs()}
	v.Inputs.Config = InputFile{Name: ".bench/config.toml", Present: true, Bytes: MaxConfigBytes, Data: bytes.Repeat([]byte{'x'}, MaxConfigBytes)}
	v.Inputs.Config.SHA256 = inputHash(v.Inputs.Config.Data)
	raw, err := v.Bytes(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParsePreparation(p, raw)
	if err != nil || !bytes.Equal(got.Inputs.Config.Data, v.Inputs.Config.Data) {
		t.Fatalf("maximum config response: %v", err)
	}
	if bytes.Contains(raw, []byte("Unregistered")) {
		t.Fatal("a registered-only root changed the canonical response bytes")
	}
	v.Unregistered = true
	dirty, err := v.Bytes(p)
	if err != nil {
		t.Fatal(err)
	}
	if got, err = ParsePreparation(p, dirty); err != nil || !got.Unregistered {
		t.Fatalf("unregistered root entry was not preserved: %v", err)
	}
	v.Unregistered = false
	v.Inputs.Member = "other"
	if _, err := v.Bytes(p); !errors.Is(err, ErrBoundary) {
		t.Fatalf("foreign member inputs accepted: %v", err)
	}
	if _, err := ParsePreparation(p, append(raw, []byte(`{}`)...)); !errors.Is(err, ErrBoundary) {
		t.Fatalf("trailing response accepted: %v", err)
	}
}
