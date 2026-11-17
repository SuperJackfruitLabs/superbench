package memberinspect

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshtree"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

// ErrBundle refuses an exported checkpoint bundle that is not exactly the
// member HEAD over the commits the mirror already holds.
var ErrBundle = errors.New("fresh checkpoint bundle refused")

// DefaultImportMemoryKiB bounds each import Git process's address space.
const DefaultImportMemoryKiB = 1 << 20

const importTimeout = 2 * time.Minute
const quarantineRef = "refs/superbench/import"

// Import makes each Fresh member's checkpoint commit present in its daemon
// mirror before the daemon pushes from that mirror. The payload exports a
// bounded bundle of only what the mirror lacks. The daemon checks its header,
// unbundles it into a private quarantine repository that borrows the mirror's
// objects, binds the quarantined ref to the inspected HEAD, then migrates only
// objects reachable from that commit into the mirror, with fsck, isolated
// configuration and bounded processes. A refused attempt leaves the mirror
// unchanged. A legacy checkout already shares its mirror's objects.
func (i Inspector) Import(ctx context.Context, state string, b *model.Bench, vector []model.WorkspaceCommit) error {
	if i.Tree == nil {
		return nil
	}
	if len(vector) != len(b.Workspace.Members) {
		return errors.New("fresh checkpoint vector differs from its members")
	}
	for n, member := range b.Workspace.Members {
		head := vector[n].Commit
		if vector[n].Name != member.Name || !model.ValidCommit(head) {
			return errors.New("fresh checkpoint vector differs from its members")
		}
		if i.present(ctx, member.Mirror, head) {
			continue
		}
		candidates := []string{member.Source}
		if cp := b.Workspace.Checkpoint; cp != nil && len(cp.Members) == len(vector) {
			candidates = append(candidates, cp.Members[n].Commit)
		}
		var exclude []string
		for _, commit := range candidates {
			if model.ValidCommit(commit) && i.present(ctx, member.Mirror, commit) {
				exclude = append(exclude, commit)
			}
		}
		raw, err := i.Tree.Export(ctx, b, member.Name, exclude)
		if err != nil {
			return err
		}
		if err = i.quarantine(ctx, state, b, member, head, exclude, raw); err != nil {
			return fmt.Errorf("member %s: %w", member.Name, err)
		}
	}
	return nil
}

func (i Inspector) present(ctx context.Context, mirror, commit string) bool {
	_, err := i.git(ctx, mirror, "cat-file", "-e", commit+"^{commit}")
	return err == nil
}

// CheckBundle accepts only a bounded v2 or v3 bundle whose single ref is HEAD
// and whose prerequisites are commits the daemon chose to exclude. Which commit
// HEAD names is bound after unbundling, against the quarantined object.
// Its object format must be the member's: a v2 bundle or v3 @object-format=sha1
// for sha1, v3 @object-format=sha256 for sha256. It returns the ref's object
// ID and the pack that follows the header.
func CheckBundle(raw []byte, format string, exclude []string) (string, []byte, error) {
	if len(raw) > freshtree.MaxExport {
		return "", nil, freshtree.ErrExportCap
	}
	end := bytes.Index(raw, []byte("\n\n"))
	if end < 0 || end > 64<<10 {
		return "", nil, ErrBundle
	}
	lines := strings.Split(string(raw[:end]), "\n")
	if lines[0] != "# v2 git bundle" && lines[0] != "# v3 git bundle" {
		return "", nil, ErrBundle
	}
	declared := "sha1" // a v2 bundle has no capabilities and is always SHA-1
	oidLength := map[string]int{"sha1": 40, "sha256": 64}[format]
	if oidLength == 0 {
		return "", nil, ErrBundle
	}
	allowed := map[string]bool{}
	for _, commit := range exclude {
		allowed[commit] = true
	}
	refs, named := 0, ""
	for _, line := range lines[1:] {
		switch {
		case strings.HasPrefix(line, "@"):
			if lines[0] != "# v3 git bundle" || line != "@object-format=sha1" && line != "@object-format=sha256" {
				return "", nil, ErrBundle
			}
			declared = strings.TrimPrefix(line, "@object-format=")
		case strings.HasPrefix(line, "-"):
			oid, _, _ := strings.Cut(line[1:], " ")
			if !allowed[oid] {
				return "", nil, ErrBundle
			}
		default:
			oid, ref, ok := strings.Cut(line, " ")
			if !ok || ref != "HEAD" || !model.ValidCommit(oid) || len(oid) != oidLength {
				return "", nil, ErrBundle
			}
			refs, named = refs+1, oid
		}
	}
	if refs != 1 || declared != format {
		return "", nil, ErrBundle
	}
	return named, raw[end+2:], nil
}

func (i Inspector) quarantine(ctx context.Context, state string, b *model.Bench, member model.WorkspaceMember, head string, exclude []string, raw []byte) error {
	named, pack, err := CheckBundle(raw, member.ObjectFormat, exclude)
	if err != nil {
		return err
	}
	parent := filepath.Join(state, "fresh-imports")
	if err := os.Mkdir(parent, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if info, err := os.Lstat(parent); err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.Join(errors.New("fresh import staging must be a private directory"), err)
	}
	dir, err := os.MkdirTemp(parent, b.Key+"-"+member.Name+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	mirror, err := filepath.Abs(member.Mirror)
	if err != nil {
		return err
	}
	repo := filepath.Join(dir, "quarantine.git")
	if _, err = i.importGit(ctx, dir, nil, "init", "--bare", "--quiet", "--template=", "--object-format="+member.ObjectFormat, repo); err != nil {
		return err
	}
	// The quarantine reads the mirror's objects; only its own new objects can
	// be written, and they disappear with it on any refusal.
	if err = os.WriteFile(filepath.Join(repo, "objects", "info", "alternates"), []byte(filepath.Join(mirror, "objects")+"\n"), 0600); err != nil {
		return err
	}
	// index-pack --strict checks every object and its links before keeping the
	// pack, in the quarantine only, whatever the installed Git does when
	// unbundling. A refused pack never reaches the mirror.
	if _, err = i.importGit(ctx, repo, pack, "index-pack", "--strict", "--fix-thin", "--stdin"); err != nil {
		return errors.Join(ErrBundle, err)
	}
	if _, err = i.importGit(ctx, repo, nil, "update-ref", quarantineRef, named); err != nil {
		return errors.Join(ErrBundle, err)
	}
	quarantined, err := i.importGit(ctx, repo, nil, "rev-parse", "--verify", quarantineRef+"^{commit}")
	if err != nil || strings.TrimSpace(quarantined) != head {
		return errors.Join(ErrBundle, errors.New("quarantined bundle does not carry the member HEAD"), err)
	}
	before, err := migrationLeftovers(mirror, nil)
	if err != nil {
		return err
	}
	if _, err = i.importGit(ctx, mirror, nil, "fetch", "--no-tags", "--no-write-fetch-head", repo, quarantineRef); err != nil {
		_, cleanup := migrationLeftovers(mirror, before)
		return errors.Join(ErrBundle, err, cleanup)
	}
	return nil
}

// migrationLeftovers lists the mirror's temporary object files. Given the
// listing from before a failed fetch, it removes only entries that fetch
// created, so a refused migration leaves no partial objects behind.
func migrationLeftovers(mirror string, before map[string]bool) (map[string]bool, error) {
	found := map[string]bool{}
	var errs []error
	for _, pattern := range []string{"objects/incoming-*", "objects/tmp_*", "objects/pack/tmp_*", "objects/pack/.tmp-*"} {
		paths, err := filepath.Glob(filepath.Join(mirror, pattern))
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			found[path] = true
			if before != nil && !before[path] {
				errs = append(errs, os.RemoveAll(path))
			}
		}
	}
	return found, errors.Join(errs...)
}

type importOutput struct{ bytes.Buffer }

func (o *importOutput) Write(p []byte) (int, error) {
	if o.Len()+len(p) > 64<<10 {
		return 0, errors.New("import Git output exceeds 64 KiB")
	}
	return o.Buffer.Write(p)
}

// importGit runs one fixed Git command with no system or global config, no
// transport but local files, fsck on every object transfer, one pack thread,
// an address-space limit on Linux and a timeout, inside the caller's Git
// control scope when it has one.
func (i Inspector) importGit(ctx context.Context, dir string, stdin []byte, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, importTimeout)
	defer cancel()
	argv := []string{"--no-replace-objects",
		"-c", "protocol.allow=never", "-c", "protocol.file.allow=always",
		"-c", "transfer.fsckObjects=true", "-c", "fetch.fsckObjects=true",
		"-c", "pack.threads=1", "-c", "index.threads=1", "-c", "core.bigFileThreshold=1m",
		"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "credential.helper=",
		"-c", "gc.auto=0", "-c", "maintenance.auto=false", "-c", "fetch.writeCommitGraph=false",
		"-C", dir}
	argv = append(argv, args...)
	limit := i.ImportMemoryKiB
	if limit == 0 {
		limit = DefaultImportMemoryKiB
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "linux" {
		cmd = exec.CommandContext(ctx, "/bin/sh", append([]string{"-c", `ulimit -v "$0" && exec /usr/bin/git "$@"`, strconv.FormatUint(limit, 10)}, argv...)...)
	} else {
		cmd = exec.CommandContext(ctx, "/usr/bin/git", argv...)
	}
	cmd.Dir = "/"
	global, err := packWindows(limit)
	if err != nil {
		return "", err
	}
	defer os.Remove(global)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + global,
		"GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_ATTR_NOSYSTEM=1", "GIT_TEMPLATE_DIR="}
	out, stderr := &importOutput{}, &importOutput{}
	cmd.Stdout, cmd.Stderr, cmd.Stdin = out, stderr, bytes.NewReader(stdin)
	cmd.WaitDelay = 2 * time.Second
	// Without a control scope a timeout must still stop every descendant.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	if err := gitcontrol.Run(ctx, cmd); err != nil {
		return "", fmt.Errorf("import git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out.String(), nil
}

// PackWindows scales Git's pack mapping to the address-space limit: each
// process maps at most a quarter of it, in windows of a sixteenth.
func PackWindows(limitKiB uint64) (window, total uint64) {
	window, total = limitKiB/16, limitKiB/4
	if window < 1<<10 {
		window = 1 << 10
	}
	if total < 4*window {
		total = 4 * window
	}
	return window, total
}

// packWindows writes the pack bounds as the import's only global config. A
// file, not -c, because the local transport clears GIT_CONFIG_PARAMETERS for
// the upload-pack and pack-objects it starts in the quarantine; they map the
// mirror's packs through the alternate and must keep the same bounds.
func packWindows(limitKiB uint64) (string, error) {
	window, total := PackWindows(limitKiB)
	f, err := os.CreateTemp("", "superbench-import-*.gitconfig")
	if err != nil {
		return "", err
	}
	_, err = fmt.Fprintf(f, "[core]\n\tpackedGitWindowSize = %dk\n\tpackedGitLimit = %dk\n\tdeltaBaseCacheLimit = %dk\n[pack]\n\tthreads = 1\n\twindowMemory = %dk\n", window, total, window, window)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
