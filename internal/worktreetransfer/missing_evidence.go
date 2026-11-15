package worktreetransfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"
)

type missingEvidence struct {
	Version, PlanDigest, Stage string
	StageIdentity              Identity
	Lock                       Entry
	Entries                    []Entry
	Copies                     []Entry
	Objects                    []RetainedObject
}

func adminSnapshot(ctx context.Context, admin string) ([]Entry, error) {
	var entries []Entry
	budget := int64(missingAdminLimit)
	err := filepath.WalkDir(admin, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == admin {
			return nil
		}
		if len(entries) >= missingEntriesLimit {
			return errors.New("missing admin entry bound exceeded")
		}
		rel, err := filepath.Rel(admin, path)
		if err != nil {
			return err
		}
		if !utf8.ValidString(rel) {
			return errors.New("invalid UTF8 admin evidence path")
		}
		v, err := snapshot(ctx, path, rel, &budget)
		if err != nil {
			return err
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			return errors.New("non-regular missing admin metadata")
		}
		entries = append(entries, v)
		return nil
	})
	return entries, err
}
func adminSummary(entries []Entry) (string, string, uint64, uint64) {
	full, protected := sha256.New(), sha256.New()
	a, b := json.NewEncoder(full), json.NewEncoder(protected)
	var size uint64
	for _, entry := range entries {
		v := struct {
			Entry Entry
			Raw   []byte
		}{entry, []byte(entry.Path)}
		a.Encode(v)
		if !mutableAdmin(entry.Path) {
			b.Encode(v)
		}
		if entry.Digest != "" {
			size += uint64(entry.Size)
		}
	}
	return hex.EncodeToString(full.Sum(nil)), hex.EncodeToString(protected.Sum(nil)), size, uint64(len(entries))
}
func mutableAdmin(path string) bool {
	return path == "HEAD" || path == "locked" || path == "logs" || path == "logs/HEAD"
}
func (m MissingManager) objects(ctx context.Context, p MissingPlan, entries []Entry) ([]RetainedObject, error) {
	if err := m.noPromisor(ctx, p); err != nil {
		return nil, err
	}
	wanted := map[string]bool{p.Spec.Commit: true}
	base := map[string]bool{}
	out, err := m.git(ctx, p.Spec.Common, p.Admin, "ls-tree", "-r", "-z", p.Spec.Commit)
	if err != nil {
		return nil, err
	}
	for _, row := range strings.Split(out, "\x00") {
		header, _, ok := strings.Cut(row, "\t")
		if !ok {
			continue
		}
		fields := strings.Fields(header)
		if len(fields) != 3 || !oid.MatchString(fields[2]) {
			return nil, errors.New("invalid retained commit tree")
		}
		base[fields[2]] = true
	}
	out, err = m.git(ctx, p.Spec.Common, p.Admin, "ls-files", "--stage", "--sparse", "-z")
	if err != nil {
		return nil, err
	}
	for _, row := range strings.Split(out, "\x00") {
		if row == "" {
			continue
		}
		header, _, ok := strings.Cut(row, "\t")
		fields := strings.Fields(header)
		if !ok || len(fields) != 3 || !oid.MatchString(fields[1]) {
			return nil, errors.New("invalid retained index")
		}
		if fields[0] != "160000" && !base[fields[1]] && !zeroOID(fields[1]) {
			wanted[fields[1]] = true
		}
	}
	for _, entry := range entries {
		if entry.Digest == "" {
			continue
		}
		name := entry.Path
		if strings.HasPrefix(name, "logs/") {
			raw, err := readBounded(filepath.Join(p.Admin, name), missingAdminLimit)
			if err != nil {
				return nil, err
			}
			for _, line := range strings.Split(string(raw), "\n") {
				if line == "" {
					continue
				}
				fields := strings.Fields(line)
				if len(fields) < 2 || !oid.MatchString(fields[0]) || !oid.MatchString(fields[1]) {
					return nil, errors.New("invalid retained reflog")
				}
				for _, v := range fields[:2] {
					if !zeroOID(v) {
						wanted[v] = true
					}
				}
			}
		} else if strings.HasPrefix(name, "refs/") || isPseudoRef(name) {
			raw, err := readBounded(filepath.Join(p.Admin, name), missingAdminLimit)
			if err != nil {
				return nil, err
			}
			if strings.HasPrefix(string(raw), "ref: ") {
				value, e := m.git(ctx, p.Spec.Common, p.Admin, "rev-parse", "--verify", name)
				if e != nil {
					return nil, e
				}
				wanted[strings.TrimSpace(value)] = true
			} else {
				for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
					fields := strings.Fields(line)
					if len(fields) > 0 && oid.MatchString(fields[0]) && !zeroOID(fields[0]) {
						wanted[fields[0]] = true
					}
				}
			}
		}
	}
	if len(wanted) > missingObjectsLimit {
		return nil, errors.New("missing object-retention bound exceeded")
	}
	keys := make([]string, 0, len(wanted))
	for key := range wanted {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]RetainedObject, 0, len(keys))
	for _, key := range keys {
		if !oid.MatchString(key) {
			return nil, errors.New("invalid retained object")
		}
		kind, e := m.git(ctx, p.Spec.Common, "", "cat-file", "-t", key)
		if e != nil {
			return nil, e
		}
		kind = strings.TrimSpace(kind)
		switch kind {
		case "commit", "tree", "blob", "tag":
		default:
			return nil, errors.New("unsupported retained object")
		}
		result = append(result, RetainedObject{key, kind})
	}
	return result, nil
}
func zeroOID(s string) bool { return strings.Trim(s, "0") == "" }
func isPseudoRef(s string) bool {
	switch s {
	case "ORIG_HEAD", "MERGE_HEAD", "REBASE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "AUTO_MERGE", "BISECT_HEAD":
		return true
	}
	return false
}
func readBounded(path string, limit int64) ([]byte, error) {
	f, e := openRegular(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	s, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if s.Size() > limit {
		return nil, errors.New("evidence file exceeds bound")
	}
	raw, e := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(raw)) > limit {
		return nil, errors.New("growing evidence exceeds bound")
	}
	return raw, e
}
func privateDir(path string) error {
	if e := physical(path); e != nil {
		return e
	}
	s, e := os.Lstat(path)
	if e != nil {
		return e
	}
	if !s.IsDir() || s.Mode().Perm() != 0700 || s.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return errors.New("evidence directory is not private")
	}
	return nil
}
func syncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func writeExclusive(path string, raw []byte) error {
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	_, e = f.Write(raw)
	if e == nil {
		e = f.Sync()
	}
	return errors.Join(e, f.Close())
}
func (m MissingManager) publishEvidence(ctx context.Context, p MissingPlan, entries []Entry) (missingEvidence, error) {
	return m.publishEvidenceWithGate(ctx, p, entries, m.guard)
}

func (m MissingManager) publishEvidenceWithGate(ctx context.Context, p MissingPlan, entries []Entry, gate func(context.Context, MissingPlan) error) (missingEvidence, error) {
	var evidence missingEvidence
	root := filepath.Dir(p.Evidence)
	if err := os.Mkdir(root, 0700); err != nil && !os.IsExist(err) {
		return evidence, err
	}
	if err := privateDir(root); err != nil {
		return evidence, err
	}
	if err := syncDir(p.Spec.Common); err != nil {
		return evidence, err
	}
	stage, err := os.MkdirTemp(root, "data-"+p.Spec.ID+"-")
	if err != nil {
		return evidence, err
	}
	// Incomplete private staging directories remain accounted evidence on failure.
	// They are never adopted or deleted to make a retry pass.
	adminCopy := filepath.Join(stage, "admin")
	if err = os.Mkdir(adminCopy, 0700); err != nil {
		return evidence, err
	}
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return evidence, err
		}
		target := filepath.Join(adminCopy, entry.Path)
		if os.FileMode(entry.Identity.Mode).IsDir() {
			if err = os.Mkdir(target, 0700); err != nil {
				return evidence, err
			}
			continue
		}
		source, e := openRegular(filepath.Join(p.Admin, entry.Path))
		if e != nil {
			return evidence, e
		}
		targetFile, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			source.Close()
			return evidence, e
		}
		h := sha256.New()
		n, e := io.Copy(io.MultiWriter(targetFile, h), io.LimitReader(contextReader{ctx, source}, entry.Size+1))
		source.Close()
		if e == nil {
			e = targetFile.Sync()
		}
		e = errors.Join(e, targetFile.Close())
		if e != nil || n != entry.Size || hex.EncodeToString(h.Sum(nil)) != entry.Digest {
			return evidence, errors.Join(e, errors.New("admin changed while retaining evidence"))
		}
	}
	if err = writeExclusive(filepath.Join(stage, "lock"), []byte(MissingVersion+"\n"+p.Digest()+"\n")); err != nil {
		return evidence, err
	}
	budget := int64(missingAdminLimit)
	lock, err := snapshot(ctx, filepath.Join(stage, "lock"), "lock", &budget)
	if err != nil {
		return evidence, err
	}
	id, err := identity(stage)
	if err != nil {
		return evidence, err
	}
	copies, err := adminSnapshot(ctx, adminCopy)
	if err != nil {
		return evidence, err
	}
	evidence = missingEvidence{Copies: copies, Version: MissingVersion, PlanDigest: p.Digest(), Stage: filepath.Base(stage), StageIdentity: id, Lock: lock, Entries: entries, Objects: p.Objects}
	raw, err := json.Marshal(evidence)
	if err != nil || len(raw) > 16<<20 {
		return evidence, errors.New("admin evidence manifest exceeds bound")
	}
	if err = writeExclusive(filepath.Join(stage, "manifest.json"), raw); err != nil {
		return evidence, err
	}
	// Sync all newly populated directories before publishing the no-clobber link.
	for i := len(entries) - 1; i >= 0; i-- {
		if os.FileMode(entries[i].Identity.Mode).IsDir() {
			if err = syncDir(filepath.Join(adminCopy, entries[i].Path)); err != nil {
				return evidence, err
			}
		}
	}
	for _, dir := range []string{adminCopy, stage} {
		if err = syncDir(dir); err != nil {
			return evidence, err
		}
	}
	if err = gate(ctx, p); err != nil {
		return evidence, err
	}
	current, err := adminSnapshot(ctx, p.Admin)
	if err != nil {
		return evidence, err
	}
	full, _, _, _ := adminSummary(current)
	if full != p.AdminDigest {
		return evidence, errors.New("admin changed before evidence publication")
	}
	if err = os.Link(filepath.Join(stage, "manifest.json"), p.Evidence); err != nil {
		return evidence, err
	}
	return evidence, syncDir(root)
}

// Missing recovery must retain locally present evidence, never fetch promised
// objects implicitly while conducting a read-only inspection.
func (m MissingManager) noPromisor(ctx context.Context, p MissingPlan) error {
	out, err := m.git(ctx, p.Spec.Common, p.Admin, "config", "--null", "--list")
	if err != nil {
		return err
	}
	for _, row := range strings.Split(out, "\x00") {
		key, _, _ := strings.Cut(row, "\n")
		key = strings.ToLower(key)
		if key == "extensions.partialclone" || strings.HasPrefix(key, "remote.") && strings.HasSuffix(key, ".promisor") {
			return errors.New("missing recovery refuses promisor repositories")
		}
	}
	return nil
}
func evidenceRoot(p MissingPlan) error {
	root := filepath.Dir(p.Evidence)
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return privateDir(root)
}
