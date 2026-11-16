package model_test

import (
	"errors"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
)

func privateScalar() *model.Bench {
	return &model.Bench{Key: "card", RegistrationID: "0123456789abcdef0123456789abcdef", PayloadLayoutVersion: payloadpolicy.LayoutVersion, AllocationRoot: "/data/benches/card", Path: "/data/benches/card/worktree", Mirror: "/data/mirrors/registered"}
}
func TestPrivateScalarAllocationIdentity(t *testing.T) {
	b := privateScalar()
	if err := benchstate.Validate(benchstate.Database{"card": b}); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*model.Bench){
		func(b *model.Bench) { b.AllocationRoot = "/elsewhere/card" },
		func(b *model.Bench) { b.Path = b.AllocationRoot },
		func(b *model.Bench) { b.AllocationRoot = "/data/benches/../benches/card" },
		func(b *model.Bench) { b.PayloadLayoutVersion = "unknown" },
		func(b *model.Bench) { b.AllocationRoot = "" },
	} {
		next := *b
		change(&next)
		if err := model.ValidateScalarLayout(&next); !errors.Is(err, model.ErrScalarLayout) {
			t.Fatalf("wrong refusal: %v", err)
		}
	}
}
func TestScalarPrivateLayoutCannotBeAdoptedOrRedirected(t *testing.T) {
	for _, private := range []bool{false, true} {
		b := privateScalar()
		if !private {
			b.PayloadLayoutVersion = ""
			b.AllocationRoot = ""
			b.Path = "/legacy/card"
		}
		old := benchstate.Capture(benchstate.Database{"card": b})
		for _, change := range []func(*model.Bench){
			func(b *model.Bench) {
				b.PayloadLayoutVersion = payloadpolicy.LayoutVersion
				b.AllocationRoot = "/data/benches/card"
				b.Path = "/data/benches/card/worktree"
			},
			func(b *model.Bench) { b.PayloadLayoutVersion = ""; b.AllocationRoot = "" },
			func(b *model.Bench) {
				b.AllocationRoot = "/other/benches/card"
				b.Path = b.AllocationRoot + "/worktree"
			},
		} {
			next := *b
			change(&next)
			if next.PayloadLayoutVersion == b.PayloadLayoutVersion && next.AllocationRoot == b.AllocationRoot && next.Path == b.Path {
				continue
			}
			if err := benchstate.ValidateTransition(old, benchstate.Database{"card": &next}); err == nil {
				t.Fatal("layout authority changed")
			}
		}
	}
}
