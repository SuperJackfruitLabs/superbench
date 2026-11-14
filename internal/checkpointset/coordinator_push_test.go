package checkpointset

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestControlPushDoesNotLaunchClientHooksOrFollowUnrelatedTags(t *testing.T) {
	f := fixtureCoordinator(t)
	marker := filepath.Join(t.TempDir(), "unfenced-client-hook")
	for _, member := range f.m.Members {
		mirror := f.c.Mirrors[member.Repository]
		hooks := filepath.Join(mirror, "private-hooks")
		if e := os.Mkdir(hooks, 0700); e != nil {
			t.Fatal(e)
		}
		// The marker path is supplied through hook-local configuration rather than
		// interpolating shell-sensitive temporary paths into executable text.
		cg(t, mirror, "config", "checkpoint.testMarker", marker)
		hook := []byte("#!/bin/sh\nmarker=$(git config checkpoint.testMarker)\nprintf started > \"$marker\"\nexit 1\n")
		if e := os.WriteFile(filepath.Join(hooks, "pre-push"), hook, 0700); e != nil {
			t.Fatal(e)
		}
		cg(t, mirror, "config", "core.hooksPath", hooks)
		cg(t, mirror, "config", "push.followTags", "true")
		cg(t, mirror, "tag", "-a", "unrelated-release", "-m", "must stay local", member.Commit)
	}
	seal, e := f.c.Complete(context.Background(), f.m)
	if e != nil {
		t.Fatal("control push ran a local hook", e)
	}
	if _, e = os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("client hook escaped the stopped-writer gate", e)
	}
	for _, member := range f.m.Members {
		mirror := f.c.Mirrors[member.Repository]
		if got, e := remoteRef(context.Background(), mirror, "refs/tags/unrelated-release"); e != nil || got != "" {
			t.Fatal("followTags published unrelated tag", got, e)
		}
		if e = confirm(context.Background(), mirror, member.PinRef, member.Commit); e != nil {
			t.Fatal("required generation pin missing", e)
		}
	}
	if seal.Commit == "" {
		t.Fatal("generation was not sealed")
	}
}

func TestControlPushStillRunsServerReceiveHooks(t *testing.T) {
	f := fixtureCoordinator(t)
	rejectRef(t, f.roots["beta"], f.m.Members[1].TaskRef)
	if _, e := f.c.Complete(context.Background(), f.m); e == nil {
		t.Fatal("server receive hook was bypassed")
	}
}
