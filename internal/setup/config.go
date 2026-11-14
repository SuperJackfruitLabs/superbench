// Package setup reads repository setup instructions and records successful preparation.
package setup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/cachepolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotapolicy"
	"io"
	"os"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
)

const MaxConfig = 64 << 10

type Config struct {
	Quota  quotapolicy.Request  `toml:"quota"`
	Caches cachepolicy.Settings `toml:"caches"`
	Setup  struct {
		Once string `toml:"once"`
		Each string `toml:"each"`
	} `toml:"setup"`
	Digest string `toml:"-"`
}

// Other unsupported fields remain strict errors during setup. They must not
// hide a valid quota request from allocation at the pinned source commit.
func quotaFromConfig(raw []byte) uint64 {
	var c struct {
		Quota quotapolicy.Request `toml:"quota"`
	}
	if toml.NewDecoder(bytes.NewReader(raw)).Decode(&c) != nil {
		return 0
	}
	n, e := c.Quota.Bytes()
	if e != nil {
		return 0
	}
	return n
}

func (c *Config) CacheSettings() cachepolicy.Settings {
	if c == nil {
		return cachepolicy.Settings{}
	}
	return c.Caches
}

// Read never follows the configuration directory/file outside the checkout.
// Missing configuration preserves the existing frozen-lockfile setup behavior.
func Read(path string) (*Config, error) {
	root, e := os.OpenRoot(path)
	if e != nil {
		return nil, e
	}
	defer root.Close()
	info, e := root.Lstat(".bench")
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if !info.IsDir() {
		return nil, errors.New(".bench must be a physical directory")
	}
	dir, e := root.OpenRoot(".bench")
	if e != nil {
		return nil, e
	}
	defer dir.Close()
	f, e := dir.OpenFile("config.toml", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	defer f.Close()
	info, e = f.Stat()
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() || info.Size() > MaxConfig {
		return nil, errors.New("setup config must be a regular file of at most 64 KiB")
	}
	raw, e := io.ReadAll(io.LimitReader(f, MaxConfig+1))
	if e != nil {
		return nil, e
	}
	if len(raw) > MaxConfig || !utf8.Valid(raw) {
		return nil, errors.New("setup config exceeds bounds or is not UTF-8")
	}
	var c Config
	if e = toml.NewDecoder(bytes.NewReader(raw)).DisallowUnknownFields().Decode(&c); e != nil {
		// Parser diagnostics can contain command text; keep it out of service errors.
		return nil, errors.New("invalid setup TOML or unsupported configuration field")
	}
	for _, command := range []string{c.Setup.Once, c.Setup.Each} {
		if len(command) > 16<<10 || strings.ContainsRune(command, 0) {
			return nil, errors.New("setup commands must be at most 16 KiB without NUL")
		}
	}
	if _, e = c.Quota.Bytes(); e != nil {
		return nil, e
	}
	sum := sha256.Sum256(raw)
	c.Digest = hex.EncodeToString(sum[:])
	return &c, nil
}

func (c Config) Hooks() bool {
	return strings.TrimSpace(c.Setup.Once) != "" || strings.TrimSpace(c.Setup.Each) != ""
}

// Commands run in separate subshells so exit/cd in once cannot skip or alter each.
// Offline is exposed explicitly; custom shell commands own their offline flags.
func (c Config) Command(done, offline bool) []string {
	script := "export BENCH_SETUP_OFFLINE=0\n"
	if offline {
		script = "export BENCH_SETUP_OFFLINE=1\n"
	}
	once := c.Setup.Once
	if done {
		once = ""
	}
	for _, command := range []string{once, c.Setup.Each} {
		if strings.TrimSpace(command) != "" {
			script += "(\n" + command + "\n)\n"
		}
	}
	return []string{"/bin/sh", "-e", "-c", script + ":\n"}
}

func Verify(path, digest string) error {
	c, e := Read(path)
	if e != nil {
		return e
	}
	return Match(c, digest)
}

// Match binds handoff validation to the same parsed configuration used for cache paths.
func Match(c *Config, digest string) error {
	if c == nil || c.Digest != digest {
		return errors.New("setup config changed during execution; success receipt not recorded")
	}
	return nil
}
