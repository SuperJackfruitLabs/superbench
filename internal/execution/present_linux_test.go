//go:build linux

package execution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Present needs no cgroup filesystem: it only names the scope directory.
func TestPresentOnlyForThisBootsExistingScope(t *testing.T) {
	identity, err := CurrentIdentity()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	e := &Execution{ID: strings.Repeat("a", 32), Root: root, BootID: identity.BootID, HostID: identity.HostID}
	if present, err := (Host{}).Present(e); err != nil || present {
		t.Fatal("absent scope reported present", present, err)
	}
	if err := os.Mkdir(filepath.Join(root, e.ID), 0700); err != nil {
		t.Fatal(err)
	}
	if present, err := (Host{}).Present(e); err != nil || !present {
		t.Fatal("existing scope reported absent", present, err)
	}
	earlier := *e
	earlier.BootID = "an-earlier-boot"
	if present, err := (Host{}).Present(&earlier); err != nil || present {
		t.Fatal("scope from an earlier boot reported present", present, err)
	}
	elsewhere := *e
	elsewhere.HostID = "another-host"
	if present, err := (Host{}).Present(&elsewhere); err == nil || present {
		t.Fatal("scope on another host was not refused", present, err)
	}
	invalid := *e
	invalid.ID = "../escape"
	if present, err := (Host{}).Present(&invalid); err == nil || present {
		t.Fatal("invalid scope ID was not refused", present, err)
	}
}
