// Package cachepolicy selects fixed shared or checkout-local cache locations.
package cachepolicy

import (
	"errors"
	"os"
	"path/filepath"
)

type Settings struct {
	Pnpm       *bool `toml:"pnpm"`
	Npm        *bool `toml:"npm"`
	Bun        *bool `toml:"bun"`
	Playwright *bool `toml:"playwright"`
	Go         *bool `toml:"go"`
}

type Paths struct{ Pnpm, Npm, Bun, Playwright, GoBuild, GoMod string }

func shared(setting *bool) bool { return setting == nil || *setting }

func (s Settings) Private() bool {
	return !shared(s.Pnpm) || !shared(s.Npm) || !shared(s.Bun) || !shared(s.Playwright) || !shared(s.Go)
}

func Select(data, checkout string, settings Settings) Paths {
	path := func(name string, enabled *bool) string {
		if shared(enabled) {
			return filepath.Join(data, "store", name)
		}
		return filepath.Join(checkout, ".bench", "cache", name)
	}
	return Paths{path("pnpm", settings.Pnpm), path("npm", settings.Npm), path("bun", settings.Bun), path("playwright", settings.Playwright), path("go-build", settings.Go), path("go-mod", settings.Go)}
}

// Prepare creates only fixed cache directories. An observed symlink, file or
// changed directory identity is refused; it is never removed or adopted.
func Prepare(data, checkout string, settings Settings) (Paths, error) {
	paths := Select(data, checkout, settings)
	for _, choice := range []struct {
		name string
		flag *bool
	}{
		{"pnpm", settings.Pnpm}, {"npm", settings.Npm}, {"bun", settings.Bun}, {"playwright", settings.Playwright}, {"go-build", settings.Go}, {"go-mod", settings.Go},
	} {
		base, parts := data, []string{"store", choice.name}
		if !shared(choice.flag) {
			base, parts = checkout, []string{".bench", "cache", choice.name}
		}
		if e := physicalDirectories(base, parts); e != nil {
			return Paths{}, e
		}
	}
	return paths, nil
}

func physicalDirectories(base string, parts []string) error {
	root, e := os.OpenRoot(base)
	if e != nil {
		return e
	}
	defer func() { root.Close() }()
	for _, name := range parts {
		if e = root.Mkdir(name, 0700); e != nil && !os.IsExist(e) {
			return e
		}
		info, e := root.Lstat(name)
		if e != nil {
			return e
		}
		if !info.IsDir() {
			return errors.New("cache path must contain only physical directories")
		}
		next, e := root.OpenRoot(name)
		if e != nil {
			return e
		}
		opened, e := next.Stat(".")
		if e != nil || !os.SameFile(info, opened) {
			next.Close()
			return errors.New("cache directory changed during preparation")
		}
		root.Close()
		root = next
	}
	return nil
}
