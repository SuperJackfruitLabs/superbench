//go:build linux && freshstoragefixture

package fixture

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
)

// refusedNext requires a next generation's Begin to refuse with no effect: no
// intent under the spec's own stem and the ledger byte for byte unchanged.
// want, when set, is the refusal it must be; not, when set, one it must not be.
func (e *env) refusedNext(s payloadbootstrap.Spec, label string, want, not error) {
	t := e.t
	t.Helper()
	ledger := filepath.Join(e.p.Ledger, "assignments.json")
	before, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	stem := filepath.Join(e.p.Ledger, "payload-bootstrap", payloadbootstrap.Stem(s)+".json")
	_, statBefore := os.Lstat(stem)
	_, err = e.next(s, label)
	if err == nil || want != nil && !errors.Is(err, want) || not != nil && errors.Is(err, not) {
		t.Fatalf("%s: next generation not refused as %v (not %v): %v", label, want, not, err)
	}
	if _, statAfter := os.Lstat(stem); errors.Is(statBefore, os.ErrNotExist) != errors.Is(statAfter, os.ErrNotExist) {
		t.Fatalf("%s: a refused next generation reserved its intent", label)
	}
	assertBytes(t, ledger, before)
}

// guardsBeforeTheNextGeneration runs once the first generation is reclaimed
// and its layout moved aside, so the deeper guards are what refuse.
func (e *env) guardsBeforeTheNextGeneration(first bench, firstIntent []byte) {
	t := e.t
	forged := successor(first, "forged")
	forged.Previous = hash("recreate-forged-previous")[:32]
	e.refusedNext(forged, "recreate-forged", payloadbootstrap.ErrBootstrap, nil)
	t.Log("LOWER_RECREATE_FORGED_PREVIOUS_REFUSED_PASS")

	skipped := successor(first, "skipped")
	skipped.Generation = 3
	e.refusedNext(skipped, "recreate-skipped", os.ErrNotExist, nil)
	t.Log("LOWER_RECREATE_GENERATION_GAP_REFUSED_PASS")

	// Synthetic predecessors, each the reclaimed intent's exact bytes under
	// another key with one binding changed. The control changes none: it passes
	// the predecessor check and is refused by the ledger, which has no
	// tombstone for that key, so each other case shows the predecessor check
	// itself refusing (ErrBootstrap, not the ledger's ErrFreshAllocation).
	bootstrapDir := filepath.Join(e.p.Ledger, "payload-bootstrap")
	rekey := func(key string, leaf bool) []byte {
		raw := bytes.Replace(firstIntent, []byte(`"Key":"`+first.spec.Key+`",`), []byte(`"Key":"`+key+`",`), 1)
		if leaf {
			raw = bytes.Replace(raw, []byte(`/benches/`+first.spec.Key+`"`), []byte(`/benches/`+key+`"`), 1)
		}
		return raw
	}
	authority := e.template.Operation.Authority
	replace := func(old, new string) func([]byte) []byte {
		return func(r []byte) []byte { return bytes.Replace(r, []byte(old), []byte(new), 1) }
	}
	same := func(r []byte) []byte { return r }
	for _, c := range []struct {
		name, stem string
		leaf       bool
		generation uint32
		change     func([]byte) []byte
		want, not  error
	}{
		{"control", "", true, 2, same, hoststorage.ErrFreshAllocation, nil},
		{"authority", "", true, 2, replace(`"Authority":"`+authority+`"`, `"Authority":"`+strings.Repeat("e", len(authority))+`"`), payloadbootstrap.ErrBootstrap, hoststorage.ErrFreshAllocation},
		{"leaf", "", false, 2, same, payloadbootstrap.ErrBootstrap, hoststorage.ErrFreshAllocation},
		{"phase", "", true, 2, replace(`"Phase":"reclaimed"`, `"Phase":"completed"`), payloadbootstrap.ErrBootstrap, hoststorage.ErrFreshAllocation},
		{"generation", "@2", true, 3, same, payloadbootstrap.ErrBootstrap, hoststorage.ErrFreshAllocation},
	} {
		key := "recreate-bind-" + c.name
		raw := c.change(rekey(key, c.leaf))
		if c.name == "authority" || c.name == "phase" {
			if bytes.Equal(raw, rekey(key, c.leaf)) {
				t.Fatalf("%s: the synthetic predecessor did not change", c.name)
			}
		}
		path := filepath.Join(bootstrapDir, key+c.stem+".json")
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		s := successor(first, c.name)
		s.Key, s.Generation = key, c.generation
		e.refusedNext(s, key, c.want, c.not)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	t.Log("LOWER_RECREATE_PREDECESSOR_BINDING_REFUSED_PASS")
}

// secondSuccessorRefused runs once the second generation exists: another
// second generation of the same predecessor, with the published layout moved
// out of the way, passes the predecessor check and is refused by the ledger,
// whose row is the live second generation, not the tombstone.
func (e *env) secondSuccessorRefused(first, second bench, layouts string) {
	t := e.t
	published, aside := filepath.Join(layouts, first.spec.Key+".json"), filepath.Join(layouts, first.spec.Key+".fixture-aside")
	if err := os.Rename(published, aside); err != nil {
		t.Fatal(err)
	}
	intent := filepath.Join(e.p.Ledger, "payload-bootstrap", payloadbootstrap.Stem(second.spec)+".json")
	before, err := os.ReadFile(intent)
	if err != nil {
		t.Fatal(err)
	}
	e.refusedNext(successor(first, "second-successor"), "recreate-second-successor", hoststorage.ErrFreshAllocation, nil)
	assertBytes(t, intent, before)
	if err = os.Rename(aside, published); err != nil {
		t.Fatal(err)
	}
	t.Log("LOWER_RECREATE_SECOND_SUCCESSOR_REFUSED_PASS")
}

// liveBenches is the ledger's capacity footprint: every bench row that still
// reserves bytes, by name. A tombstone or an earlier generation reserves none.
func (e *env) liveBenches() map[string]hoststorage.Assignment {
	live := map[string]hoststorage.Assignment{}
	for name, a := range e.ledger().Assignments {
		if strings.HasPrefix(name, "bench:") && a.Bytes != 0 {
			live[name] = a
		}
	}
	return live
}

// assertFootprint requires a group of cases to leave the ledger's reservations
// as it found them, so that every later case, here and in the parent, has the
// fixture volume's whole capacity. It names any reservation left behind.
func (e *env) assertFootprint(before map[string]hoststorage.Assignment, cases string) {
	e.t.Helper()
	after := e.liveBenches()
	var changed []string
	for name, a := range after {
		if before[name] != a {
			changed = append(changed, name+" left reserved")
		}
	}
	for name := range before {
		if _, ok := after[name]; !ok {
			changed = append(changed, name+" released")
		}
	}
	if len(changed) != 0 {
		e.t.Fatalf("%s changed the volume's reservations: %s", cases, strings.Join(changed, ", "))
	}
}
