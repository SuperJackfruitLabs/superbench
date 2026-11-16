package payloadexec

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func launchRequest() Request {
	return Request{Version: Version, PolicyDigest: strings.Repeat("d", 64), Kind: "payload", Key: "card", LayoutID: strings.Repeat("a", 32), Member: "api", Scope: Scope{ID: strings.Repeat("b", 32), HostID: strings.Repeat("c", 64), BootID: "12345678-1234-1234-1234-123456789abc"}, Arguments: []string{"sh", "-c", "printf '%s' \"$1\"", "sh", ""}}
}

func TestRequestCannotSelectCredentialsPolicyCgroupRootOrWorkdir(t *testing.T) {
	request := launchRequest()
	raw, err := request.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseRequest(raw)
	if err != nil || len(parsed.Arguments) != 5 || parsed.Arguments[4] != "" {
		t.Fatalf("valid request: %#v %v", parsed, err)
	}
	for _, field := range []string{`"uid":0`, `"policy":"/tmp/other"`, `"cgroup_root":"/sys/fs/cgroup/other"`, `"workdir":"/var/lib/superbench"`, `"key":"other"`} {
		changed := append(append([]byte(nil), raw[:len(raw)-1]...), []byte(","+field+"}")...)
		if _, err := ParseRequest(changed); !errors.Is(err, ErrRequest) {
			t.Fatalf("unexpected field %s: %v", field, err)
		}
	}
	for _, change := range []func(*Request){
		func(r *Request) { r.Key = "../other" },
		func(r *Request) { r.Kind = "root" },
		func(r *Request) { r.PolicyDigest = "unknown" },
		func(r *Request) { r.Member = "../other" },
		func(r *Request) { r.LayoutID = strings.Repeat("a", 31) },
		func(r *Request) { r.Root = true },
		func(r *Request) { r.Root, r.Member, r.Kind = true, "", "git" },
		func(r *Request) { r.Scope.HostID = "foreign" },
		func(r *Request) { r.Arguments = []string{""} },
		func(r *Request) { r.Arguments = []string{"sh", "\x00"} },
	} {
		candidate := launchRequest()
		change(&candidate)
		if _, err := candidate.Bytes(); !errors.Is(err, ErrRequest) {
			t.Fatalf("invalid request: %#v %v", candidate, err)
		}
	}
}

func TestRootPayloadSelectionHasNoMemberOrGitAmbiguity(t *testing.T) {
	r := launchRequest()
	r.Root, r.Member = true, ""
	raw, err := r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseRequest(raw)
	if err != nil || !got.Root || got.Member != "" || got.Kind != "payload" {
		t.Fatalf("root payload selection: %#v %v", got, err)
	}
}

func TestRequestRejectsAmbiguousAndOversizedInputBeforeLaunch(t *testing.T) {
	raw, err := launchRequest().Bytes()
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range [][]byte{
		append(append([]byte(nil), raw...), raw...),
		bytes.Replace(raw, []byte(`"api"`), []byte(`null`), 1),
		bytes.Replace(raw, []byte(`"api"`), []byte(`"\ud800"`), 1),
		append(append([]byte(nil), raw...), bytes.Repeat([]byte(" "), MaxRequest)...),
	} {
		if _, err := ParseRequest(invalid); !errors.Is(err, ErrRequest) {
			t.Fatalf("invalid request accepted: %v", err)
		}
	}
}
