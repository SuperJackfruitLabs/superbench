package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveConfigurationStrict(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"", "manual"}, {"[archive]\n", "manual"},
		{"[archive]\nwhen='manual'", "manual"},
		{"[archive]\nwhen='branch-merged'", "branch-merged"},
	} {
		c, err := Read(configFile(t, tc.raw))
		if err != nil || c.ArchiveWhen() != tc.want {
			t.Fatalf("%q: %v %v", tc.raw, c, err)
		}
	}
	if (*Config)(nil).ArchiveWhen() != "manual" {
		t.Fatal("absent configuration opted in")
	}
	for _, raw := range []string{
		"[archive]\nwhen=''", "[archive]\nwhen='automatic'", "[archive]\nwhen=3",
		"[archive]\nwhen=false", "[archive]\nwhen=['manual']", "[archive]\nwhenn='manual'",
		"archive='manual'", "[archive]\nwhen={value='manual'}", "[archive]\nwhen='MANUAL'",
	} {
		if _, err := Read(configFile(t, raw)); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestAtCommittedConfigurationMatchesRead(t *testing.T) {
	root, first := quotaRepo(t, "[archive]\nwhen='manual'\n")
	firstConfig, err := At(context.Background(), root, first)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".bench/config.toml")
	if err = os.WriteFile(path, []byte("[archive]\nwhen='branch-merged'\n"), 0644); err != nil {
		t.Fatal(err)
	}
	second := quotaCommit(t, root)
	committed, err := At(context.Background(), root, second)
	if err != nil || committed.ArchiveWhen() != "branch-merged" || committed.Digest == firstConfig.Digest {
		t.Fatal(committed, err)
	}
	local, err := Read(root)
	if err != nil || local.Digest != committed.Digest {
		t.Fatal(local, err)
	}
	for _, flag := range []string{"--assume-unchanged", "--skip-worktree"} {
		quotaGit(t, root, "update-index", flag, ".bench/config.toml")
		if err = os.WriteFile(path, []byte("[archive]\nwhen='manual'\n"), 0644); err != nil {
			t.Fatal(err)
		}
		// Git's clean result cannot authorize a repository archive preference.
		if out := quotaGit(t, root, "status", "--porcelain"); out != "" {
			t.Fatal(out)
		}
		local, err = Read(root)
		object, readErr := At(context.Background(), root, second)
		if err != nil || readErr != nil || local.Digest == object.Digest || object.ArchiveWhen() != "branch-merged" {
			t.Fatal(local, object, err, readErr)
		}
		if err = os.WriteFile(path, []byte("[archive]\nwhen='branch-merged'\n"), 0644); err != nil {
			t.Fatal(err)
		}
		quotaGit(t, root, "update-index", "--no-assume-unchanged", "--no-skip-worktree", ".bench/config.toml")
	}
}

func TestAtStrictBoundsAndObjects(t *testing.T) {
	for _, kind := range []string{"missing", "empty", "symlink", "tree", "executable", "max-size", "oversize", "utf8", "invalid", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			root, _ := quotaRepo(t, "[archive]\nwhen='branch-merged'\n")
			path := filepath.Join(root, ".bench/config.toml")
			var err error
			wantErr := true
			switch kind {
			case "missing":
				err = os.Remove(path)
				wantErr = false
			case "empty":
				err = os.WriteFile(path, nil, 0644)
				wantErr = false
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
				wantErr = false
			case "max-size", "oversize":
				n := MaxConfig
				if kind == "oversize" {
					n++
				} else {
					wantErr = false
				}
				err = os.WriteFile(path, []byte("#"+strings.Repeat("x", n-1)), 0644)
			case "utf8":
				err = os.WriteFile(path, []byte{255}, 0644)
			case "invalid":
				err = os.WriteFile(path, []byte("[archive]\nwhen=''"), 0644)
			case "unknown":
				err = os.WriteFile(path, []byte("[archive]\nwhen='branch-merged'\nunknown='secret'"), 0644)
			}
			if err != nil {
				t.Fatal(err)
			}
			source := quotaCommit(t, root)
			c, err := At(context.Background(), root, source)
			if (err != nil) != wantErr {
				t.Fatalf("config=%v error=%v wantError=%v", c, err, wantErr)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("config leaked", err)
			}
			if kind == "missing" && c != nil {
				t.Fatal("missing config returned preference")
			}
			if !wantErr && kind != "missing" && c == nil {
				t.Fatal("present empty config treated as absent")
			}
		})
	}
}

func TestAtCancellationAndGitErrors(t *testing.T) {
	root, source := quotaRepo(t, "[archive]\nwhen='branch-merged'\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := At(ctx, root, source); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{root, "missing-source"}, {t.TempDir(), source}} {
		if _, err := At(context.Background(), pair[0], pair[1]); err == nil {
			t.Fatal("Git failure treated as absent")
		}
	}
	blob := quotaGit(t, root, "rev-parse", source+":.bench/config.toml")
	if err := os.Remove(filepath.Join(root, ".git/objects", blob[:2], blob[2:])); err != nil {
		t.Fatal(err)
	}
	if _, err := At(context.Background(), root, source); err == nil {
		t.Fatal("missing object treated as absent")
	}
}

func TestBoundedOutputCannotBypassWriter(t *testing.T) {
	root, _ := quotaRepo(t, "#"+strings.Repeat("x", 100))
	if _, err := boundedOutput(context.Background(), root, 8, "show", "HEAD:.bench/config.toml"); err == nil {
		t.Fatal("output bound bypassed")
	}
}
