package setup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotapolicy"
)

func quotaGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func quotaRepo(t *testing.T, raw string) (string, string) {
	t.Helper()
	root := configFile(t, raw)
	quotaGit(t, root, "init", "--quiet")
	return root, quotaCommit(t, root)
}

func quotaCommit(t *testing.T, root string) string {
	t.Helper()
	quotaGit(t, root, "add", "--all")
	quotaGit(t, root, "commit", "--quiet", "--allow-empty", "-m", "quota fixture")
	return quotaGit(t, root, "rev-parse", "HEAD")
}

func TestMatchQuotaPreservesPinnedAllocation(t *testing.T) {
	size := "3KiB"
	c := &Config{Quota: quotapolicy.Request{Size: &size}}
	for _, tc := range []struct {
		name   string
		c      *Config
		pinned uint64
		ok     bool
	}{
		{"absent", nil, 0, true}, {"unset", &Config{}, 0, true}, {"equal", c, 3072, true},
		{"removed", nil, 3072, false}, {"unset-after-custom", &Config{}, 3072, false},
		{"added", c, 0, false}, {"changed", c, 4096, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := MatchQuota(tc.c, tc.pinned); (err == nil) != tc.ok {
				t.Fatalf("MatchQuota = %v, want success %v", err, tc.ok)
			}
		})
	}
	size = "invalid"
	if err := MatchQuota(c, 0); err == nil {
		t.Fatal("invalid quota treated as an unset request")
	}
}

func TestQuotaAtUsesCommittedSource(t *testing.T) {
	root, first := quotaRepo(t, "[quota]\nsize='3KiB'\n")
	path := filepath.Join(root, ".bench/config.toml")
	if err := os.WriteFile(path, []byte("[quota]\nsize='5KiB'\n"), 0644); err != nil {
		t.Fatal(err)
	}
	second := quotaCommit(t, root)
	if err := os.WriteFile(path, []byte("[quota]\nsize='7KiB'\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for commit, want := range map[string]uint64{first: 3072, second: 5120} {
		if n, err := QuotaAt(root, commit); err != nil || n != want {
			t.Fatalf("quota at %s = %d, %v; want %d", commit, n, err, want)
		}
	}
}

func TestQuotaSelectionIgnoresUnrelatedInvalidFields(t *testing.T) {
	for _, raw := range []string{
		"[quota]\nsize='3KiB'\n[setup]\nonce=123\n",
		"[quota]\nsize='3KiB'\n[caches]\npnpm='wrong'\n",
		"setup=123\ncaches=false\n[quota]\nsize='3KiB'\n",
		"[quota]\nsize='3KiB'\n[setup]\nunknown='bad'\n",
		"[quota]\nsize='3KiB'\n[archive]\nwhen='invalid'\n",
		"[quota]\nsize='3KiB'\n[archive]\nwhen=123\n",
		"[quota]\nsize='3KiB'\n[archive]\nunknown='bad'\n",
	} {
		root, commit := quotaRepo(t, raw)
		if n, err := QuotaAt(root, commit); err != nil || n != 3072 {
			t.Fatalf("unrelated invalid fields hid quota: %d, %v", n, err)
		}
		if _, err := Read(root); err == nil {
			t.Fatal("strict setup validation accepted invalid fields")
		}
	}
}

func TestQuotaAtBoundsAndFileTypes(t *testing.T) {
	for _, kind := range []string{"missing", "symlink", "tree", "executable", "max-size", "oversize", "invalid-toml", "invalid-quota", "invalid-utf8"} {
		t.Run(kind, func(t *testing.T) {
			root, _ := quotaRepo(t, "[quota]\nsize='3KiB'\n")
			path := filepath.Join(root, ".bench/config.toml")
			want := uint64(0)
			var err error
			switch kind {
			case "missing":
				err = os.Remove(path)
			case "symlink":
				if err = os.Remove(path); err == nil {
					err = os.Symlink("../../outside", path)
				}
			case "tree":
				if err = os.Remove(path); err == nil {
					err = os.Mkdir(path, 0755)
				}
				if err == nil {
					err = os.WriteFile(filepath.Join(path, "nested"), []byte("fixture"), 0644)
				}
			case "executable":
				err = os.Chmod(path, 0755)
				want = 3072
			case "max-size", "oversize":
				raw := "[quota]\nsize='3KiB'\n#"
				n := MaxConfig
				if kind == "oversize" {
					n++
				} else {
					want = 3072
				}
				err = os.WriteFile(path, []byte(raw+strings.Repeat("x", n-len(raw))), 0644)
			case "invalid-toml":
				err = os.WriteFile(path, []byte("[quota"), 0644)
			case "invalid-quota":
				err = os.WriteFile(path, []byte("[quota]\nsize='0G'\n"), 0644)
			case "invalid-utf8":
				err = os.WriteFile(path, []byte{255}, 0644)
			}
			if err != nil {
				t.Fatal(err)
			}
			commit := quotaCommit(t, root)
			if n, err := QuotaAt(root, commit); err != nil || n != want {
				t.Fatalf("quota for %s = %d, %v; want %d", kind, n, err, want)
			}
		})
	}
}

func TestQuotaAtReportsGitFailures(t *testing.T) {
	root, commit := quotaRepo(t, "[quota]\nsize='3KiB'\n")
	for _, tc := range []struct{ root, source string }{
		{filepath.Join(t.TempDir(), "missing"), commit}, {t.TempDir(), commit}, {root, "nonexistent-source"},
	} {
		if n, err := QuotaAt(tc.root, tc.source); err == nil || n != 0 {
			t.Fatalf("Git failure treated as default: %d, %v", n, err)
		}
	}
	blob := quotaGit(t, root, "rev-parse", commit+":.bench/config.toml")
	if err := os.Remove(filepath.Join(root, ".git/objects", blob[:2], blob[2:])); err != nil {
		t.Fatal(err)
	}
	if n, err := QuotaAt(root, commit); err == nil || n != 0 {
		t.Fatalf("unreadable blob treated as default: %d, %v", n, err)
	}
	if _, err := Read(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("setup IO failure treated as absent configuration")
	}
}
