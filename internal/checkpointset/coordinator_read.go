package checkpointset

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Recover reads an exact historical seal and its immutable pins, never task tips.
// Every repository must already have a configured identity/mirror; manifest data
// cannot add a remote or select arbitrary filesystem storage.
func (c Coordinator) Recover(ctx context.Context, repo, benchID, seal, digest string) (Manifest, error) {
	var none Manifest
	if !identifier.MatchString(benchID) || (!sha1Pattern.MatchString(seal) && !sha256Pattern.MatchString(seal)) || !sha256Pattern.MatchString(digest) {
		return none, errors.New("invalid checkpoint recovery identity")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	path, e := c.mirror(ctx, repo)
	if e != nil {
		return none, e
	}
	meta, _ := MetadataRef(benchID)
	tip, e := remoteRef(ctx, path, meta)
	if e != nil || tip == "" {
		return none, errors.New("checkpoint metadata branch is missing")
	}
	if _, e = gitText(ctx, path, "fetch", "--no-tags", "--no-write-fetch-head", "origin", meta); e != nil {
		return none, e
	}
	if _, e = gitText(ctx, path, "merge-base", "--is-ancestor", seal, tip); e != nil {
		return none, errors.New("checkpoint seal is not on its metadata branch")
	}
	m, e := readManifest(ctx, path, seal, digest)
	if e != nil {
		return none, e
	}
	if m.BenchID != benchID || coordinatorMember(m).Repository != repo {
		return none, errors.New("checkpoint coordinator identity differs")
	}
	for _, v := range m.Members {
		mirror, e := c.mirror(ctx, v.Repository)
		if e != nil {
			return none, e
		}
		format, e := gitText(ctx, mirror, "rev-parse", "--show-object-format")
		if e != nil || format != v.ObjectFormat {
			return none, errors.New("checkpoint recovery object format differs")
		}
		if e = confirm(ctx, mirror, v.PinRef, v.Commit); e != nil {
			return none, e
		}
		if _, e = gitText(ctx, mirror, "fetch", "--no-tags", "--no-write-fetch-head", "origin", v.PinRef); e != nil {
			return none, e
		}
		kind, e := gitText(ctx, mirror, "cat-file", "-t", v.Commit)
		if e != nil || kind != "commit" {
			return none, errors.New("checkpoint pinned commit unavailable")
		}
		if e = confirm(ctx, mirror, v.PinRef, v.Commit); e != nil {
			return none, e
		}
	}
	return m, nil
}
func readManifest(ctx context.Context, path, commit, digest string) (Manifest, error) {
	var none Manifest
	kind, e := gitText(ctx, path, "cat-file", "-t", commit)
	if e != nil || kind != "commit" {
		return none, errors.New("checkpoint seal is not a commit")
	}
	tree, e := runGit(ctx, path, nil, nil, "ls-tree", "-z", commit)
	if e != nil {
		return none, e
	}
	rows := strings.Split(tree, "\x00")
	if len(rows) != 2 || rows[1] != "" {
		return none, errors.New("checkpoint seal tree must contain one file")
	}
	entry := strings.SplitN(rows[0], "\t", 2)
	if len(entry) != 2 || entry[1] != "checkpoint.json" {
		return none, errors.New("checkpoint seal tree differs")
	}
	fields := strings.Fields(entry[0])
	if len(fields) != 3 || fields[0] != "100644" || fields[1] != "blob" {
		return none, errors.New("checkpoint manifest must be a regular blob")
	}
	size, e := gitText(ctx, path, "cat-file", "-s", fields[2])
	if e != nil {
		return none, e
	}
	// Bound output as well as input before parsing; the Git runner caps pipes.
	n, err := strconv.Atoi(size)
	if err != nil || n < 1 || n > MaxRaw {
		return none, errors.New("checkpoint manifest exceeds bounds")
	}
	raw, e := runGit(ctx, path, nil, nil, "cat-file", "blob", fields[2])
	if e != nil {
		return none, e
	}
	if len(raw) > MaxRaw || digest != "" && Digest([]byte(raw)) != digest {
		return none, errors.New("checkpoint manifest digest differs")
	}
	m, e := Parse([]byte(raw))
	if e != nil {
		return none, e
	}
	parents, e := gitText(ctx, path, "rev-list", "--parents", "-n", "1", commit)
	if e != nil {
		return none, e
	}
	ids := strings.Fields(parents)
	if previous(m) == "" {
		if len(ids) != 1 {
			return none, errors.New("genesis checkpoint has a parent")
		}
	} else if len(ids) != 2 || ids[1] != previous(m) {
		return none, errors.New("checkpoint predecessor differs from commit parent")
	}
	format, e := gitText(ctx, path, "rev-parse", "--show-object-format")
	if e != nil || format != coordinatorMember(m).ObjectFormat {
		return none, errors.New("checkpoint seal object format differs")
	}
	return m, nil
}
