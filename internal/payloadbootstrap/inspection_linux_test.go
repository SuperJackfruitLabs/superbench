//go:build linux

package payloadbootstrap

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestBootstrapInspectionRequiresInstalledScopedAuthority(t *testing.T) {
	_, spec, _ := resultFixture(t)
	for _, manager := range []*Manager{nil, {}, {digest: "invented"}} {
		got, err := manager.Inspect(context.Background(), spec)
		if !errors.Is(err, ErrBootstrap) || !reflect.DeepEqual(got, Inspection{}) {
			t.Fatalf("raw manager returned evidence: %+v %v", got, err)
		}
	}
}
