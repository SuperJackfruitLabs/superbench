package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCacheSettingsAreTypedAndPartOfPreparationIdentity(t *testing.T) {
	path := configFile(t, "[setup]\nonce='true'\n[caches]\npnpm=false\nplaywright=true\n")
	config, e := Read(path)
	if e != nil || config.Caches.Pnpm == nil || *config.Caches.Pnpm || config.Caches.Playwright == nil || !*config.Caches.Playwright {
		t.Fatal(config, e)
	}
	if e = os.WriteFile(filepath.Join(path, "pnpm-lock.yaml"), []byte("fixture"), 0600); e != nil {
		t.Fatal(e)
	}
	args, e := FrozenCommand(path, "/shared", true)
	if e != nil || !strings.Contains(strings.Join(args, " "), filepath.Join(path, ".bench/cache/pnpm")) {
		t.Fatal(args, e)
	}
	if e = os.WriteFile(filepath.Join(path, ".bench/config.toml"), []byte("[setup]\nonce='true'\n[caches]\npnpm=true\nplaywright=true\n"), 0600); e != nil {
		t.Fatal(e)
	}
	changed, e := Read(path)
	if e != nil {
		t.Fatal(e)
	}
	if Match(changed, config.Digest) == nil || Match(nil, config.Digest) == nil {
		t.Fatal("stale configuration admitted to handoff")
	}
	if e = Verify(path, config.Digest); e == nil {
		t.Fatal("cache policy change retained old preparation identity")
	}
	for _, raw := range []string{"[caches]\npnpm='secret'", "[caches]\nplaywright=1", "[caches]\npnpm=true\npnpm=false", "[caches]\npath='/outside'"} {
		if _, e = Read(configFile(t, raw)); e == nil || strings.Contains(e.Error(), "secret") {
			t.Fatal("invalid cache configuration accepted or exposed", e)
		}
	}
}
