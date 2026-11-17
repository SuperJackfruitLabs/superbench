package memberinspect

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshtree"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/registeredinspection"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/stageexec"
)

var isolated = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
	"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid"}

func run(t *testing.T, dir string, input []byte, args ...string) string {
	t.Helper()
	cmd := exec.Command("/usr/bin/git", append([]string{"-C", dir}, args...)...)
	cmd.Env, cmd.Stdin = isolated, bytes.NewReader(input)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

func daemonGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/git", append([]string{"-C", dir}, args...)...)
	cmd.Env = isolated
	out, err := cmd.Output()
	return string(out), err
}

// exporter is the payload side: it returns exactly the bytes a test chose.
type exporter struct {
	raw   []byte
	calls int
}

func (e *exporter) Root(context.Context, *model.Bench, bool) error { return errors.New("unused") }
func (e *exporter) Git(context.Context, *model.Bench, string, ...string) (string, error) {
	return "", errors.New("unused")
}
func (e *exporter) Inputs(context.Context, *model.Bench, string) (registeredinspection.Inputs, error) {
	return registeredinspection.Inputs{}, errors.New("unused")
}
func (e *exporter) Transport(*model.Bench, string) (stageexec.PrepareTransport, error) {
	return nil, errors.New("unused")
}
func (e *exporter) Export(context.Context, *model.Bench, string, []string) ([]byte, error) {
	e.calls++
	return e.raw, nil
}

type importFixture struct {
	t                     *testing.T
	state, source, mirror string
	base, head            string
	bench                 *model.Bench
}

func newImport(t *testing.T) *importFixture { return newFormat(t, "sha1") }

func newFormat(t *testing.T, format string) *importFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &importFixture{t: t, state: filepath.Join(root, "state"), source: filepath.Join(root, "source"), mirror: filepath.Join(root, "mirror.git")}
	if err = os.Mkdir(f.state, 0700); err != nil {
		t.Fatal(err)
	}
	run(t, root, nil, "init", "-q", "-b", "main", "--object-format="+format, f.source)
	run(t, f.source, nil, "commit", "-q", "--allow-empty", "-m", "base")
	f.base = strings.TrimSpace(run(t, f.source, nil, "rev-parse", "HEAD"))
	run(t, root, nil, "clone", "-q", "--bare", f.source, f.mirror)
	if err = os.WriteFile(filepath.Join(f.source, "work.txt"), []byte("payload work\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run(t, f.source, nil, "add", "work.txt")
	run(t, f.source, nil, "commit", "-q", "-m", "payload work")
	f.head = strings.TrimSpace(run(t, f.source, nil, "rev-parse", "HEAD"))
	f.bench = benchFor(f)
	f.bench.Workspace.Members[0].ObjectFormat = format
	return f
}

func benchFor(f *importFixture) *model.Bench {
	return &model.Bench{Key: "card", Workspace: &model.Workspace{Members: []model.WorkspaceMember{{Name: "a", Mirror: f.mirror, Source: f.base, ObjectFormat: "sha1"}}}}
}

func (f *importFixture) bundle(args ...string) []byte {
	return []byte(run(f.t, f.source, nil, append([]string{"bundle", "create", "-"}, args...)...))
}

func (f *importFixture) objects() string { return run(f.t, f.mirror, nil, "count-objects", "-v") }

func (f *importFixture) importing(raw []byte, head string, memory uint64) (*exporter, error) {
	e := &exporter{raw: raw}
	i := Inspector{Git: daemonGit, Tree: e, ImportMemoryKiB: memory}
	return e, i.Import(context.Background(), f.state, f.bench, []model.WorkspaceCommit{{Name: "a", Commit: head}})
}

func (f *importFixture) unchanged(name string, before string, err error) {
	f.t.Helper()
	if !errors.Is(err, ErrBundle) && !errors.Is(err, freshtree.ErrExportCap) {
		f.t.Fatalf("%s: hostile bundle was not refused: %v", name, err)
	}
	if after := f.objects(); after != before {
		f.t.Fatalf("%s: refused import changed the mirror:\n%s\n%s", name, before, after)
	}
	if entries, e := os.ReadDir(filepath.Join(f.state, "fresh-imports")); e != nil && !os.IsNotExist(e) || len(entries) != 0 {
		f.t.Fatalf("%s: refused import left staging behind: %v %v", name, entries, e)
	}
}

func TestImportMigratesOnlyTheInspectedHeadWithoutARef(t *testing.T) {
	f := newImport(t)
	e, err := f.importing(f.bundle("HEAD", "--not", f.base), f.head, 0)
	if err != nil || e.calls != 1 {
		t.Fatalf("honest bundle refused: %v (exports %d)", err, e.calls)
	}
	run(t, f.mirror, nil, "cat-file", "-e", f.head+"^{commit}")
	if refs := run(t, f.mirror, nil, "for-each-ref", "--format=%(refname)"); refs != "refs/heads/main\n" {
		t.Fatalf("import changed mirror refs: %q", refs)
	}
	if e, err = f.importing(nil, f.head, 0); err != nil || e.calls != 0 {
		t.Fatalf("a head the mirror holds was exported again: %v (exports %d)", err, e.calls)
	}
}

func TestImportRefusesHostileBundlesAndLeavesTheMirrorUnchanged(t *testing.T) {
	f := newImport(t)
	// Another commit on a side branch: an honest-looking bundle for the wrong HEAD.
	run(t, f.source, nil, "checkout", "-q", "-b", "side", f.base)
	run(t, f.source, nil, "commit", "-q", "--allow-empty", "-m", "other")
	other := f.bundle("HEAD", "--not", f.base)
	otherCommit := strings.TrimSpace(run(t, f.source, nil, "rev-parse", "HEAD"))
	run(t, f.source, nil, "checkout", "-q", "main")
	run(t, f.source, nil, "commit", "-q", "--allow-empty", "-m", "between")
	between := strings.TrimSpace(run(t, f.source, nil, "rev-parse", "HEAD"))
	unexpected := f.bundle("HEAD", "--not", f.head) // its prerequisite is not in the mirror
	run(t, f.source, nil, "reset", "-q", "--hard", f.head)
	tree := strings.TrimSpace(run(t, f.source, nil, "rev-parse", f.head+"^{tree}"))
	corrupt := strings.TrimSpace(run(t, f.source, []byte("tree "+tree+"\nparent "+f.base+"\nauthor A <a@example.invalid> 1 +0000\n\nno committer\n"), "hash-object", "-t", "commit", "-w", "--literally", "--stdin"))
	run(t, f.source, nil, "update-ref", "refs/heads/corrupt", corrupt)
	// A prerequisite the mirror holds but the daemon did not choose to exclude.
	run(t, f.source, nil, "checkout", "-q", "-b", "known", f.base)
	run(t, f.source, nil, "commit", "-q", "--allow-empty", "-m", "known")
	run(t, f.source, nil, "push", "-q", f.mirror, "known")
	known := strings.TrimSpace(run(t, f.source, nil, "rev-parse", "HEAD"))
	run(t, f.source, nil, "commit", "-q", "--allow-empty", "-m", "over known")
	overKnown := strings.TrimSpace(run(t, f.source, nil, "rev-parse", "HEAD"))
	unchosen := f.bundle("HEAD", "--not", known)
	run(t, f.source, nil, "checkout", "-q", "main")
	honest := f.bundle("HEAD", "--not", f.base)
	headLine := []byte(f.head + " HEAD\n")
	duplicated := bytes.Replace(honest, headLine, append(append([]byte(nil), headLine...), headLine...), 1)
	// bundle create cannot rename a ref; rewrite the ref line, keeping the pack.
	corruptRaw := bytes.Replace(f.bundle("refs/heads/corrupt", "--not", f.base), []byte(corrupt+" refs/heads/corrupt\n"), []byte(corrupt+" HEAD\n"), 1)
	before := f.objects()
	for name, tc := range map[string]struct {
		raw  []byte
		head string
	}{
		"missing HEAD ref":              {f.bundle("main", "--not", f.base), f.head},
		"extra ref":                     {f.bundle("HEAD", "main", "--not", f.base), f.head},
		"unexpected prerequisite":       {unexpected, between},
		"another commit":                {other, f.head},
		"unchosen prerequisite":         {unchosen, overKnown},
		"duplicate HEAD ref":            {duplicated, f.head},
		"oversized":                     {make([]byte, freshtree.MaxExport+1), f.head},
		"not a bundle":                  {[]byte("PACK\x00\x00\x00\x02"), f.head},
		"truncated pack":                {f.bundle("HEAD", "--not", f.base)[:200], f.head},
		"corrupt object (no committer)": {corruptRaw, corrupt},
	} {
		_, err := f.importing(tc.raw, tc.head, 0)
		f.unchanged(name, before, err)
	}
	if _, err := daemonGit(context.Background(), f.mirror, "cat-file", "-e", otherCommit+"^{commit}"); err == nil {
		t.Fatal("a bundle for another commit reached the mirror")
	}
	if _, err := f.importing(make([]byte, freshtree.MaxExport+1), f.head, 0); !errors.Is(err, freshtree.ErrExportCap) {
		t.Fatalf("oversized bundle did not name the cap: %v", err)
	}
}

// deltaBomb is a valid pack whose one delta declares an 8 GiB result from a
// tiny base: resolving it would allocate that much in index-pack.
func deltaBomb(head string) []byte {
	var pack bytes.Buffer
	pack.WriteString("PACK")
	_ = binary.Write(&pack, binary.BigEndian, uint32(2))
	_ = binary.Write(&pack, binary.BigEndian, uint32(2))
	header := func(kind byte, size uint64) {
		b := kind<<4 | byte(size&0x0f)
		size >>= 4
		for size != 0 {
			pack.WriteByte(b | 0x80)
			b = byte(size & 0x7f)
			size >>= 7
		}
		pack.WriteByte(b)
	}
	deflate := func(raw []byte) {
		w := zlib.NewWriter(&pack)
		_, _ = w.Write(raw)
		_ = w.Close()
	}
	base := bytes.Repeat([]byte{0}, 1024)
	baseOffset := pack.Len()
	header(3, uint64(len(base)))
	deflate(base)
	varint := func(out *bytes.Buffer, v uint64) {
		for v >= 0x80 {
			out.WriteByte(byte(v) | 0x80)
			v >>= 7
		}
		out.WriteByte(byte(v))
	}
	var delta bytes.Buffer
	varint(&delta, uint64(len(base)))
	varint(&delta, 8<<30)
	delta.Write([]byte{1, 'x'})
	deltaOffset := pack.Len()
	header(6, uint64(delta.Len()))
	distance := uint64(deltaOffset - baseOffset)
	encoded := []byte{byte(distance & 0x7f)}
	for distance >>= 7; distance != 0; distance >>= 7 {
		distance--
		encoded = append([]byte{byte(0x80 | distance&0x7f)}, encoded...)
	}
	pack.Write(encoded)
	deflate(delta.Bytes())
	sum := sha1.Sum(pack.Bytes())
	pack.Write(sum[:])
	return append([]byte("# v2 git bundle\n"+head+" HEAD\n\n"), pack.Bytes()...)
}

func TestImportBoundsADeltaBomb(t *testing.T) {
	f := newImport(t)
	before := f.objects()
	_, err := f.importing(deltaBomb(f.head), f.head, 256<<10)
	f.unchanged("delta bomb", before, err)
	if runtime.GOOS == "linux" && !strings.Contains(err.Error(), "llocate") {
		t.Fatalf("the address-space bound did not stop the delta allocation: %v", err)
	}
}

func TestImportCarriesSHA256MembersAndRefusesAFormatMismatch(t *testing.T) {
	f := newFormat(t, "sha256")
	honest := f.bundle("HEAD", "--not", f.base)
	if !bytes.HasPrefix(honest, []byte("# v3 git bundle\n@object-format=sha256\n")) {
		t.Fatalf("fixture did not make a SHA-256 bundle: %q", honest[:40])
	}
	if _, err := f.importing(honest, f.head, 0); err != nil {
		t.Fatalf("honest SHA-256 bundle refused: %v", err)
	}
	run(t, f.mirror, nil, "cat-file", "-e", f.head+"^{commit}")

	sha1 := newFormat(t, "sha1")
	before := sha1.objects()
	_, err := sha1.importing(honest, sha1.head, 0)
	sha1.unchanged("SHA-256 bundle for a SHA-1 member", before, err)

	other := newFormat(t, "sha256")
	before = other.objects()
	_, err = other.importing(sha1.bundle("HEAD", "--not", sha1.base), other.head, 0)
	other.unchanged("SHA-1 bundle for a SHA-256 member", before, err)
	for _, header := range []string{"# v2 git bundle\n", "# v3 git bundle\n@object-format=sha1\n"} {
		if _, _, err := CheckBundle([]byte(header+f.head+" HEAD\n\nPACK"), "sha256", nil); !errors.Is(err, ErrBundle) {
			t.Fatalf("%q accepted for a SHA-256 member: %v", header, err)
		}
	}
	if _, _, err := CheckBundle([]byte("# v3 git bundle\n@object-format=sha256\n"+sha1.head+" HEAD\n\nPACK"), "sha256", nil); !errors.Is(err, ErrBundle) {
		t.Fatalf("a SHA-1 object ID was accepted under a SHA-256 header: %v", err)
	}
}

func TestImportOutputAndStagingAreBounded(t *testing.T) {
	var out importOutput
	if _, err := out.Write(make([]byte, 64<<10)); err != nil {
		t.Fatal(err)
	}
	if _, err := out.Write([]byte{0}); err == nil {
		t.Fatal("import Git output exceeded 64 KiB")
	}
	f := newImport(t)
	if err := os.Mkdir(filepath.Join(f.state, "fresh-imports"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := f.importing(f.bundle("HEAD", "--not", f.base), f.head, 0); err == nil || !strings.Contains(err.Error(), "private directory") {
		t.Fatalf("a shared staging parent was used: %v", err)
	}
}

func TestMigrationLeftoversRemovesOnlyWhatTheFailedFetchCreated(t *testing.T) {
	mirror := t.TempDir()
	pack := filepath.Join(mirror, "objects", "pack")
	if err := os.MkdirAll(pack, 0700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(pack, "tmp_pack_other")
	if err := os.WriteFile(old, nil, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := migrationLeftovers(mirror, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{filepath.Join(pack, "tmp_pack_new"), filepath.Join(mirror, "objects", "incoming-x"), filepath.Join(mirror, "objects", "tmp_obj_y")} {
		if err = os.WriteFile(name, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = migrationLeftovers(mirror, before); err != nil {
		t.Fatal(err)
	}
	after, err := migrationLeftovers(mirror, nil)
	if err != nil || len(after) != 1 || !after[old] {
		t.Fatalf("leftovers after cleanup: %v %v", after, err)
	}
}
