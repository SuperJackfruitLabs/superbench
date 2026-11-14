package archivepolicy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func TestArchiveCheckPrecedesIntentAndDurableRetrySkipsCheck(t *testing.T) {
	data := t.TempDir()
	path := filepath.Join(data, "benches", "task")
	mirror := filepath.Join(data, "mirror")
	admin := filepath.Join(mirror, "worktrees", "task")
	for _, dir := range []string{path, admin} {
		if e := os.MkdirAll(dir, 0700); e != nil {
			t.Fatal(e)
		}
	}
	file := filepath.Join(path, "retained")
	if e := os.WriteFile(file, []byte("keep this work"), 0600); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0700); _ = os.Chmod(file, 0600) })
	b := model.Bench{Key: "task", Path: path, Mirror: mirror, Repo: "repo", Branch: "card/task", Checkpoint: "commit"}
	journal := filepath.Join(data, "intent.json")
	locked, writes, updates := false, 0, 0
	interrupted := errors.New("interrupted after durable intent")
	interrupt := false
	a := Archiver{
		Update: func(key string, action func(*model.Bench) error) error {
			if key != "task" {
				t.Fatal(key)
			}
			updates++
			if interrupt && updates == 2 {
				return interrupted
			}
			copy := b
			locked = true
			e := action(&copy)
			locked = false
			if e == nil {
				b = copy
			}
			return e
		},
		Journal: func(string) string { return journal },
		SaveIntent: func(path string, raw []byte) error {
			if !locked {
				t.Fatal("intent outside transaction")
			}
			writes++
			return os.WriteFile(path, raw, 0600)
		},
		Busy: errors.New("busy"),
		Inspector: Inspector{Data: data, MirrorPath: func(string) string { return mirror }, ValidKey: func(key string) bool { return key == "task" },
			Clean: func(*model.Bench) error { return nil }, Confirmed: func(*model.Bench, string) error { return nil },
			Git: func(_ string, args ...string) (string, error) {
				switch strings.Join(args, " ") {
				case "rev-parse HEAD":
					return "commit", nil
				case "symbolic-ref --quiet HEAD":
					return "refs/heads/card/task", nil
				case "rev-parse --path-format=absolute --git-common-dir":
					return mirror, nil
				case "rev-parse --absolute-git-dir":
					return admin, nil
				case "remote get-url origin":
					return "repo", nil
				default:
					t.Fatalf("unexpected git %v", args)
					return "", nil
				}
			},
		},
	}
	b.Lease = &model.Lease{}
	if _, e := a.Archive("task", time.Hour, func(*model.Bench) error {
		t.Fatal("busy archive evaluated trigger")
		return nil
	}); !errors.Is(e, a.Busy) {
		t.Fatal("busy error identity changed", e)
	}
	b.Lease = nil
	denied := errors.New("branch not merged")
	if _, e := a.Archive("task", time.Hour, func(*model.Bench) error {
		if !locked {
			t.Fatal("check outside transaction")
		}
		return denied
	}); !errors.Is(e, denied) {
		t.Fatal(e)
	}
	if writes != 0 || b.ArchivedAt != "" {
		t.Fatal("denied check created durable intent", writes, b)
	}
	updates = 0
	interrupt = true
	checks := 0
	out, e := a.Archive("task", time.Hour, func(*model.Bench) error { checks++; return nil })
	if !errors.Is(e, interrupted) || out == nil || b.ArchivedAt == "" || b.ArchiveFreezeDigest == "" || b.ArchiveFrozenAt != "" {
		t.Fatal("missing durable archive intent", out, e)
	}
	archived, deadline, digest := b.ArchivedAt, b.ReapAfter, b.ArchiveFreezeDigest
	interrupt = false
	out, e = a.Archive("task", 2*time.Hour, func(*model.Bench) error { t.Fatal("retry reevaluated trigger"); return denied })
	if e != nil || out.ArchiveFrozenAt == "" || checks != 1 || writes != 1 {
		t.Fatal(out, e, checks, writes)
	}
	if b.ArchivedAt != archived || b.ReapAfter != deadline || b.ArchiveFreezeDigest != digest {
		t.Fatal("retry changed retained archive intent")
	}
	raw, e := os.ReadFile(file)
	if e != nil || string(raw) != "keep this work" {
		t.Fatal("archive lost data", string(raw), e)
	}
}
