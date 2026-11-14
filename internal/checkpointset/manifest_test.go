package checkpointset

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func fixture() Manifest {
	id, generation := strings.Repeat("a", 32), strings.Repeat("b", 32)
	pin, _ := PinRef(id, generation, "api")
	return Manifest{Version: Version, BenchID: id, GenerationID: generation, CheckpointRepository: "api", Members: []Member{
		{Name: "api", Repository: "https://example.invalid/repository.git", TaskRef: "refs/heads/task", PinRef: pin, ObjectFormat: "sha1", Commit: strings.Repeat("c", 40)},
	}}
}

func rawFixture(t *testing.T, m Manifest) []byte {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestManifestRoundTripAndRawDigest(t *testing.T) {
	m := fixture()
	for _, format := range []string{"sha1", "sha256"} {
		m.Members[0].ObjectFormat = format
		m.Members[0].Commit = strings.Repeat("c", 40)
		previous := strings.Repeat("d", 40)
		if format == "sha256" {
			m.Members[0].Commit = strings.Repeat("c", 64)
			previous = strings.Repeat("d", 64)
		}
		m.PreviousManifestCommit = &previous
		raw := rawFixture(t, m)
		got, err := Parse(raw)
		if err != nil || got.Members[0].Commit != m.Members[0].Commit {
			t.Fatal(got, err)
		}
		indented := &bytes.Buffer{}
		if err = json.Indent(indented, raw, "", " "); err != nil {
			t.Fatal(err)
		}
		if _, err = Parse(indented.Bytes()); err != nil {
			t.Fatal(err)
		}
		if Digest(raw) == Digest(indented.Bytes()) || len(Digest(raw)) != 64 {
			t.Fatal("digest did not bind exact JSON bytes")
		}
	}
	if Digest(nil) != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatal("not SHA256")
	}
	m = fixture()
	raw := rawFixture(t, m)
	if bytes.Contains(raw, []byte("previous_manifest_commit")) {
		t.Fatal("genesis serialized previous commit")
	}
	// A coordinator can use SHA256 while another member independently uses SHA1.
	member := m.Members[0]
	member.Name = "web"
	member.PinRef, _ = PinRef(m.BenchID, m.GenerationID, "web")
	member.Repository = "ssh://workspace@example.invalid/web"
	m.Members = append(m.Members, member)
	m.Members[0].ObjectFormat = "sha256"
	m.Members[0].Commit = strings.Repeat("e", 64)
	previous := strings.Repeat("d", 64)
	m.PreviousManifestCommit = &previous
	if _, err := Parse(rawFixture(t, m)); err != nil {
		t.Fatal(err)
	}
}

func TestManifestIdentityRefsAndBounds(t *testing.T) {
	tests := map[string]func(*Manifest){
		"version":             func(m *Manifest) { m.Version = "other" },
		"bench":               func(m *Manifest) { m.BenchID = strings.Repeat("A", 32) },
		"generation":          func(m *Manifest) { m.GenerationID = "short" },
		"coordinator-missing": func(m *Manifest) { m.CheckpointRepository = "missing" },
		"coordinator-case":    func(m *Manifest) { m.CheckpointRepository = "API" },
		"empty-members":       func(m *Manifest) { m.Members = nil },
		"member-name":         func(m *Manifest) { m.Members[0].Name = "bad.name" },
		"member-name-long":    func(m *Manifest) { m.Members[0].Name = strings.Repeat("a", 65) },
		"task-short":          func(m *Manifest) { m.Members[0].TaskRef = "task" },
		"task-tag":            func(m *Manifest) { m.Members[0].TaskRef = "refs/tags/task" },
		"task-lock":           func(m *Manifest) { m.Members[0].TaskRef = "refs/heads/task.lock" },
		"task-reflog":         func(m *Manifest) { m.Members[0].TaskRef = "refs/heads/task@{0}" },
		"task-control":        func(m *Manifest) { m.Members[0].TaskRef = "refs/heads/task\x7f" },
		"pin-generation": func(m *Manifest) {
			m.Members[0].PinRef = strings.ReplaceAll(m.Members[0].PinRef, m.GenerationID, strings.Repeat("d", 32))
		},
		"pin-branch": func(m *Manifest) {
			m.Members[0].PinRef = strings.Replace(m.Members[0].PinRef, "refs/tags/", "refs/heads/", 1)
		},
		"format":           func(m *Manifest) { m.Members[0].ObjectFormat = "SHA1" },
		"commit-length":    func(m *Manifest) { m.Members[0].Commit = strings.Repeat("a", 64) },
		"commit-uppercase": func(m *Manifest) { m.Members[0].Commit = strings.Repeat("C", 40) },
		"previous-empty":   func(m *Manifest) { s := ""; m.PreviousManifestCommit = &s },
		"previous-format":  func(m *Manifest) { s := strings.Repeat("a", 64); m.PreviousManifestCommit = &s },
		"duplicate-name":   func(m *Manifest) { m.Members = append(m.Members, m.Members[0]) },
		"duplicate-task": func(m *Manifest) {
			n := m.Members[0]
			n.Name = "web"
			n.PinRef, _ = PinRef(m.BenchID, m.GenerationID, n.Name)
			m.Members = append(m.Members, n)
		},
		"unsorted": func(m *Manifest) {
			n := m.Members[0]
			n.Name = "aaa"
			n.PinRef, _ = PinRef(m.BenchID, m.GenerationID, n.Name)
			n.Repository = "/other"
			m.Members = append(m.Members, n)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			m := fixture()
			mutate(&m)
			if m.Validate() == nil {
				t.Fatal("invalid manifest validated")
			}
			if _, err := Parse(rawFixture(t, m)); err == nil {
				t.Fatal("invalid manifest parsed")
			}
		})
	}
	for _, count := range []int{32, 33} {
		m := fixture()
		m.Members = nil
		for i := 0; i < count; i++ {
			name := string(rune('a'+i/10)) + string(rune('0'+i%10))
			pin, _ := PinRef(m.BenchID, m.GenerationID, name)
			m.Members = append(m.Members, Member{Name: name, Repository: "/repository " + name, TaskRef: "refs/heads/task", PinRef: pin, ObjectFormat: "sha1", Commit: strings.Repeat("c", 40)})
		}
		m.CheckpointRepository = m.Members[0].Name
		if err := m.Validate(); (err == nil) != (count == 32) {
			t.Fatal(count, err)
		}
	}
}

func TestRepositoryIdentities(t *testing.T) {
	valid := []string{
		"http://example.invalid/repo", "https://example.invalid/space%20repo", "https://example.invalid",
		"ssh://workspace@example.invalid/path", "ssh://example.invalid:22/path", "workspace@example.invalid:path with spaces", "workspace@[::1]:repo.git", "workspace@[2001:db8::1]:repo.git",
		"/srv/repo with spaces", "/Users/workspace/repo", "file:///srv/repo%20name", "file://localhost/srv/repo",
		"/" + strings.Repeat("a", 4095), "/srv/naïve",
	}
	invalid := []string{
		"", "-option", "relative/repo", "./relative", "../relative", "C:\\repo", "repo\nname", "/repo\rname", "/repo\x00name", "/" + strings.Repeat("a", 4096), string([]byte{'/', 255}),
		"https://user@example.invalid/repo", "http://user:password@example.invalid/repo", "http://@example.invalid/repo",
		"https://example.invalid/repo?", "https://example.invalid/repo#", "https://example.invalid/repo?query", "https://example.invalid/repo#fragment", "https:///missing-host",
		"ssh://user:password@example.invalid/repo", "ssh://user:@example.invalid/repo", "ssh://example.invalid/repo?", "ssh://example.invalid/repo#",
		"file://remote/srv/repo", "file://user@localhost/srv/repo", "file://localhost/srv/repo?", "file://localhost/srv/repo#", "file:relative", "file:///repo?query",
		"ftp://example.invalid/repo", "unknown:repo", "host:repo", "user:password@host:repo", "user@@host:repo", "@host:repo", "user@:repo", "user@host:", "ssh://@example.invalid/repo", "user@[::1]:", "user@[bad]:repo", "user@[::1]repo",
	}
	for _, repository := range valid {
		m := fixture()
		m.Members[0].Repository = repository
		if err := m.Validate(); err != nil {
			t.Fatalf("valid identity rejected %q: %v", repository, err)
		}
	}
	for _, repository := range invalid {
		m := fixture()
		m.Members[0].Repository = repository
		if err := m.Validate(); err == nil {
			t.Fatalf("invalid identity accepted %q", repository)
		}
	}
}

func TestStrictRawJSON(t *testing.T) {
	raw := string(rawFixture(t, fixture()))
	samples := []string{
		"", "null", "[]", raw + " {}", raw + " false", raw + " trailing",
		strings.Replace(raw, `"version":`, `"extra":"secret","version":`, 1),
		strings.Replace(raw, `"name":`, `"extra":"secret","name":`, 1),
		strings.Replace(raw, `"version":`, `"VERSION":`, 1),
		strings.Replace(raw, `"name":`, `"NAME":`, 1),
		strings.Replace(raw, `"version":`, `"version":"secret","version":`, 1),
		strings.Replace(raw, `"name":`, `"name":"secret","name":`, 1),
		strings.Replace(raw, `"name":`, `"\u006eame":"secret","name":`, 1),
		strings.Replace(raw, `"members":[`, `"members":null,"ignored":[`, 1),
		strings.Replace(raw, `"members":[`, `"members":[null,`, 1),
		strings.Replace(raw, `"version":`, `"previous_manifest_commit":null,"version":`, 1),
		strings.Replace(raw, `"name":"api"`, `"name":1.5`, 1),
		strings.Replace(raw, `"name":"api"`, `"name":true`, 1),
		strings.Replace(raw, `"name":"api"`, `"name":null`, 1),
		strings.Replace(raw, `"commit":`, `"commit":{},"ignored":`, 1),
		string([]byte{255}) + raw, strings.Repeat(" ", MaxRaw) + raw,
	}
	for i, sample := range samples {
		if _, err := Parse([]byte(sample)); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("case %d accepted or exposed input: %v", i, err)
		}
	}
	padded := []byte(raw + strings.Repeat(" ", MaxRaw-len(raw)))
	if _, err := Parse(padded); err != nil {
		t.Fatal("exact byte limit rejected", err)
	}
	if _, err := Parse(append(padded, ' ')); err == nil {
		t.Fatal("over byte limit accepted")
	}
	// JSON order is not canonicalized before hashing or required for acceptance.
	reordered := `{"members":` + strings.Split(raw, `"members":`)[1]
	reordered = strings.TrimSuffix(reordered, "}") + `,"version":"superbench.checkpoint.v1","bench_id":"` + fixture().BenchID + `","generation_id":"` + fixture().GenerationID + `","checkpoint_repository":"api"}`
	if _, err := Parse([]byte(reordered)); err != nil {
		t.Fatal("key order rejected", err)
	}
}

func TestDeterministicRefs(t *testing.T) {
	m := fixture()
	metadata, err := MetadataRef(m.BenchID)
	if err != nil || metadata != "refs/heads/superbench/checkpoints/"+m.BenchID {
		t.Fatal(metadata, err)
	}
	pin, err := PinRef(m.BenchID, m.GenerationID, "api")
	if err != nil || pin != m.Members[0].PinRef {
		t.Fatal(pin, err)
	}
	if _, err = MetadataRef(strings.Repeat("A", 32)); err == nil {
		t.Fatal("invalid metadata ID accepted")
	}
	for _, args := range [][3]string{{"", m.GenerationID, "api"}, {m.BenchID, "bad", "api"}, {m.BenchID, m.GenerationID, "../bad"}} {
		if _, err = PinRef(args[0], args[1], args[2]); err == nil {
			t.Fatal("invalid pin accepted")
		}
	}
}

// The default corpus is pinned from a committed published treaty revision.
// An explicit candidate path permits development checks without changing the pin.
func TestSharedCheckpointCorpus(t *testing.T) {
	path := os.Getenv("SUPERBENCH_CHECKPOINT_CORPUS")
	if path == "" {
		path = "testdata/checkpoints.json"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Version string `json:"version"`
		Valid   []struct {
			Name string          `json:"name"`
			Body json.RawMessage `json:"body"`
		} `json:"valid"`
		Invalid []struct {
			Name   string          `json:"name"`
			Reason string          `json:"reason"`
			Body   json.RawMessage `json:"body"`
		} `json:"invalid"`
	}
	if err = json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.Version != "superbench.checkpoint-fixtures.v1" || len(corpus.Valid) == 0 || len(corpus.Invalid) == 0 {
		t.Fatal("invalid corpus envelope")
	}
	for _, sample := range corpus.Valid {
		t.Run("valid/"+sample.Name, func(t *testing.T) {
			if _, err := Parse(sample.Body); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, sample := range corpus.Invalid {
		t.Run("invalid/"+sample.Name, func(t *testing.T) {
			if _, err := Parse(sample.Body); err == nil {
				t.Fatal("invalid candidate fixture accepted", sample.Reason)
			}
		})
	}
}

func TestUnicodeScalarsRemainLossless(t *testing.T) {
	raw := string(rawFixture(t, fixture()))
	for _, escaped := range []string{`\ud800`, `\udc00`, `\ud800x`, `\ud800\ud800`, `\ud800\u0041`, `\udc00\ud800`} {
		for _, replacement := range []struct{ old, new string }{
			{`"refs/heads/task"`, `"refs/heads/` + escaped + `"`},
			{`"https://example.invalid/repository.git"`, `"/srv/` + escaped + `"`},
		} {
			if _, err := Parse([]byte(strings.Replace(raw, replacement.old, replacement.new, 1))); err == nil {
				t.Fatal("lossy surrogate accepted", escaped)
			}
		}
	}
	for _, name := range []string{`\ud83d\ude80`, "🚀", "�"} {
		sample := strings.Replace(raw, `"refs/heads/task"`, `"refs/heads/`+name+`"`, 1)
		if _, err := Parse([]byte(sample)); err != nil {
			t.Fatal("valid scalar or literal escape rejected", name, err)
		}
	}
	literal := strings.Replace(raw, `"https://example.invalid/repository.git"`, `"/srv/literal\\ud800"`, 1)
	if _, err := Parse([]byte(literal)); err != nil {
		t.Fatal("literal escaped backslash rejected", err)
	}
	m := fixture()
	m.Members[0].Repository = "/srv/�"
	if err := m.Validate(); err != nil {
		t.Fatal("literal replacement character rejected", err)
	}
	for _, mutate := range []func(*Manifest){
		func(m *Manifest) { m.Members[0].Repository = "/srv/" + string([]byte{255}) },
		func(m *Manifest) { m.Members[0].TaskRef = "refs/heads/" + string([]byte{255}) },
		func(m *Manifest) { m.Members[0].Name = string([]byte{255}) },
		func(m *Manifest) { m.Version = string([]byte{255}) },
	} {
		m := fixture()
		mutate(&m)
		if m.Validate() == nil {
			t.Fatal("invalid typed UTF8 string accepted")
		}
	}
}

func TestSCPOpaquePathAndASCIIIdentity(t *testing.T) {
	for _, repository := range []string{"user@host:path:with spaces/?#", "user@host:path@other", "user@[::1]:path:with spaces/?#"} {
		m := fixture()
		m.Members[0].Repository = repository
		if err := m.Validate(); err != nil {
			t.Fatal("opaque SCP path rejected", repository, err)
		}
	}
	for _, repository := range []string{"usér@host:path", "user@höst:path", "user@-host:path", "user@host_name:path", "user!@host:path"} {
		m := fixture()
		m.Members[0].Repository = repository
		if err := m.Validate(); err == nil {
			t.Fatal("invalid SCP authority accepted", repository)
		}
	}
}
