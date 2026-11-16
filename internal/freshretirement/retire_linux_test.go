//go:build linux

package freshretirement

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
)

func TestRetirementRequiresDirectRootBeforeStorageRead(t *testing.T) {
	if os.Getuid() == 0 || os.Geteuid() == 0 {
		t.Fatal("ordinary fixture requires an unprivileged user")
	}
	p, _, a := recordsFixture()
	got, err := ObserveAbsent(context.Background(), p, a.Key)
	if !errors.Is(err, ErrRetirement) || !reflect.DeepEqual(got, Approval{}) {
		t.Fatalf("ordinary observation returned authority: %+v %v", got, err)
	}
	retired, err := RetireAbsent(context.Background(), p, a)
	if !errors.Is(err, ErrRetirement) || retired != (hoststorage.Assignment{}) {
		t.Fatalf("ordinary retirement changed storage: %+v %v", retired, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := ObserveAbsent(ctx, p, a.Key); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, Approval{}) {
		t.Fatalf("cancelled inspection reached storage: %+v %v", got, err)
	}
}
