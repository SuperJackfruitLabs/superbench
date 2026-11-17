//go:build linux

package memberinspect

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A mirror pack larger than the import's address-space limit must still be
// readable: Git maps it in small windows, never whole.
func TestImportReadsAMirrorPackLargerThanItsMemoryLimit(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &importFixture{t: t, state: filepath.Join(root, "state"), source: filepath.Join(root, "source"), mirror: filepath.Join(root, "mirror.git")}
	if err = os.Mkdir(f.state, 0700); err != nil {
		t.Fatal(err)
	}
	run(t, root, nil, "init", "-q", "-b", "main", f.source)
	large := make([]byte, 256<<20) // incompressible, so the mirror's one pack is about 256 MiB
	if _, err = rand.Read(large); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(f.source, "large.bin"), large, 0600); err != nil {
		t.Fatal(err)
	}
	run(t, f.source, nil, "add", "large.bin")
	run(t, f.source, nil, "commit", "-q", "-m", "large")
	f.base = strings.TrimSpace(run(t, f.source, nil, "rev-parse", "HEAD"))
	run(t, root, nil, "clone", "-q", "--bare", "--no-local", f.source, f.mirror)
	run(t, f.source, nil, "commit", "-q", "--allow-empty", "-m", "payload work")
	f.head = strings.TrimSpace(run(t, f.source, nil, "rev-parse", "HEAD"))
	f.bench = benchFor(f)
	if _, err = f.importing(f.bundle("HEAD", "--not", f.base), f.head, 192<<10); err != nil {
		t.Fatalf("a mirror pack above the 192 MiB limit blocked the import: %v", err)
	}
	run(t, f.mirror, nil, "cat-file", "-e", f.head+"^{commit}")
}
