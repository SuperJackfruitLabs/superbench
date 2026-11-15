package operationcontrol

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestControlProvenanceIsPinnedBeforePlanningAndCannotBeRebound(t *testing.T) {
	db := store(t)
	i := intent("control")
	b := record()
	base, err := WithScope(context.Background(), i, func(fn func() error) error { return fn() })
	if err != nil {
		t.Fatal(err)
	}
	c := Control{State: "/state", Root: "/scope", HostID: strings.Repeat("f", 64), BootID: "boot"}
	ctx, err := WithControl(base, c)
	if err != nil {
		t.Fatal(err)
	}
	commit(t, db, func(tx *sql.Tx) error { return AdmitContext(ctx, tx, i.Key, "") })
	got, err := Get(db, i.Target)
	if err != nil || got.Control != c || got.Phase != "admitted" {
		t.Fatal("planning lacks immutable provenance", got, err)
	}
	commit(t, db, func(tx *sql.Tx) error { return BindContext(ctx, tx, b) })
	if err = GuardContext(base, db, b); !errors.Is(err, ErrAuthority) {
		t.Fatal("cooperative context adopted managed authority", err)
	}
	changed := c
	changed.Root = "/other-scope"
	other, err := WithControl(base, changed)
	if err != nil {
		t.Fatal(err)
	}
	if err = GuardContext(other, db, b); !errors.Is(err, ErrAuthority) {
		t.Fatal("another root adopted operation", err)
	}
	if _, err = WithControl(ctx, changed); err == nil {
		t.Fatal("configured control context rebound")
	}
	if err = GuardContext(ctx, db, b); err != nil {
		t.Fatal(err)
	}
}
func TestPartialControlProvenanceCannotAuthorizeAdmission(t *testing.T) {
	base, err := WithScope(context.Background(), intent("partial"), func(fn func() error) error { return fn() })
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []Control{{Root: "/scope"}, {State: "/state", Root: "relative", HostID: strings.Repeat("a", 64), BootID: "boot"}, {State: "/state", Root: "/scope", HostID: strings.Repeat("a", 64), BootID: ""}} {
		if _, err = WithControl(base, c); err == nil {
			t.Fatal("partial control accepted", c)
		}
	}
}
