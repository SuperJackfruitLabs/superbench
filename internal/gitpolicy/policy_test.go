package gitpolicy

import (
	"errors"
	"testing"
)

func TestManagedPrefixAndLeadingOptionsAreClosed(t *testing.T) {
	good := Arguments("status", "--porcelain")
	if err := Validate(good); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]string{{"git", "status"}, Arguments("-c", "core.hooksPath=/tmp/hooks", "status"), Arguments("diff", "--ext-diff"), Arguments("show", "--textconv")} {
		if !errors.Is(Validate(bad), ErrPolicy) {
			t.Fatalf("unsafe argv accepted: %q", bad)
		}
	}
	for i := 0; i < len(Arguments()); i++ {
		bad := append([]string(nil), good...)
		bad[i] = "changed"
		if !errors.Is(Validate(bad), ErrPolicy) {
			t.Fatalf("prefix position %d not enforced", i)
		}
	}
	if err := Validate(Arguments("diff", "HEAD")); err != nil {
		t.Fatal(err)
	}
	bad := Arguments("diff", "HEAD")
	bad[len(Arguments())+1] = "--ext-diff"
	if !errors.Is(Validate(bad), ErrPolicy) {
		t.Fatal("diff protections were optional")
	}
}
