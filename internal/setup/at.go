package setup

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var errConfigBlob = errors.New("configuration must be a regular UTF-8 blob of at most 64 KiB")

// At reads strict configuration from Git objects, independently of checkout or
// index flags. Callers can compare its digest with Read before granting authority.
func At(ctx context.Context, mirror, source string) (*Config, error) {
	raw, err := configBlob(ctx, mirror, source)
	if err != nil || raw == nil {
		return nil, err
	}
	return parseConfig(raw)
}

// boundedOutput also bounds Git output if an object is malformed or changes
// between the size query and content read. Parser errors never expose contents.
func boundedOutput(ctx context.Context, mirror string, limit int, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"--no-replace-objects", "-C", mirror}, args...)...)
	command.WaitDelay = 2 * time.Second
	out := &boundedBuffer{limit: limit}
	command.Stdout = out
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return out.buffer.Bytes(), nil
}

type boundedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *boundedBuffer) Write(raw []byte) (int, error) {
	if len(raw) > b.limit-b.buffer.Len() {
		return 0, errConfigBlob
	}
	return b.buffer.Write(raw)
}

func configBlob(ctx context.Context, mirror, source string) ([]byte, error) {
	listing, err := boundedOutput(ctx, mirror, 1024, "ls-tree", "-z", source, "--", ".bench/config.toml")
	if err != nil {
		return nil, err
	}
	if len(listing) == 0 {
		return nil, nil
	}
	entry := strings.TrimSuffix(string(listing), "\x00")
	metadata, path, ok := strings.Cut(entry, "\t")
	fields := strings.Fields(metadata)
	if !ok || path != ".bench/config.toml" || len(fields) != 3 {
		return nil, errors.New("invalid configuration tree entry")
	}
	if (fields[0] != "100644" && fields[0] != "100755") || fields[1] != "blob" {
		return nil, errConfigBlob
	}
	size, err := boundedOutput(ctx, mirror, 64, "cat-file", "-s", fields[2])
	if err != nil {
		return nil, err
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(size)), 10, 64)
	if err != nil {
		return nil, err
	}
	if n > MaxConfig {
		return nil, errConfigBlob
	}
	raw, err := boundedOutput(ctx, mirror, MaxConfig, "cat-file", "blob", fields[2])
	if err != nil {
		return nil, err
	}
	if uint64(len(raw)) != n || !utf8.Valid(raw) {
		return nil, errConfigBlob
	}
	if raw == nil {
		raw = []byte{}
	}
	return raw, nil
}
