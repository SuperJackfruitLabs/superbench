package remoteca

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math/big"
	"testing"

	"golang.org/x/crypto/ssh"
)

// edL is the order of the prime subgroup.
var edL, _ = new(big.Int).SetString("7237005577332262213973186563042994240857116359379907606001950938285454250989", 10)

func edIdentity() edPoint { return edPoint{x: big.NewInt(0), y: big.NewInt(1)} }

func (a edPoint) times(k *big.Int) edPoint {
	r := edIdentity()
	for i := k.BitLen() - 1; i >= 0; i-- {
		r = r.add(r)
		if k.Bit(i) == 1 {
			r = r.add(a)
		}
	}
	return r
}

func (a edPoint) identity() bool { return a.x.Sign() == 0 && a.y.Cmp(big.NewInt(1)) == 0 }

func (a edPoint) encode() ed25519.PublicKey {
	be := a.y.FillBytes(make([]byte, 32))
	out := make(ed25519.PublicKey, 32)
	for i, b := range be {
		out[31-i] = b
	}
	out[31] |= byte(a.x.Bit(0)) << 7
	return out
}

func edLine(t *testing.T, key ed25519.PublicKey) string {
	t.Helper()
	k, err := ssh.NewPublicKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(ssh.MarshalAuthorizedKey(k))
}

// torsion returns the eight points of the small-order subgroup: [L]P has
// order dividing 8 for any point P; a P whose [L]P has order 8 generates it.
func torsion(t *testing.T) []ed25519.PublicKey {
	t.Helper()
	for range 64 {
		pub, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		p, ok := decodeEd25519(pub)
		if !ok {
			t.Fatal("a generated key does not decode")
		}
		// A generated key is in the prime subgroup; add a point of the
		// curve's other torsion by stepping y until a full-order point.
		for y := int64(2); ; y++ {
			var enc [32]byte
			enc[0] = byte(y)
			q, ok := decodeEd25519(enc[:])
			if !ok {
				continue
			}
			g := p.add(q).times(edL)
			if g.times(big.NewInt(4)).identity() {
				continue
			}
			var points []ed25519.PublicKey
			r := edIdentity()
			for range 8 {
				points = append(points, r.encode())
				r = r.add(g)
			}
			if !r.identity() {
				t.Fatal("[8]g is not the identity")
			}
			return points
		}
	}
	t.Fatal("no order-8 point found")
	return nil
}

func TestClientKeyOfSmallOrderIsRefused(t *testing.T) {
	a, _ := openTemp(t)
	points := torsion(t)
	seen := map[string]bool{}
	for _, key := range points {
		seen[hex.EncodeToString(key)] = true
		if _, err := parseClientKey(edLine(t, key)); !errors.Is(err, ErrKey) {
			t.Fatalf("small-order key %x accepted: %v", key, err)
		}
		if _, err := a.IssueWriter(edLine(t, key), testClaims(), testNow); !errors.Is(err, ErrKey) {
			t.Fatalf("certificate issued to small-order key %x: %v", key, err)
		}
	}
	// libsodium's published order-8 point, independent of the arithmetic here.
	known := "26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05"
	if len(seen) != 8 || !seen[known] && !seen[known[:62]+"85"] {
		t.Fatal("torsion points do not include the published order-8 point", seen)
	}
}

func TestClientKeyThatIsNoPointIsRefused(t *testing.T) {
	p := func(s string) ed25519.PublicKey { b, _ := hex.DecodeString(s); return b }
	for _, key := range []ed25519.PublicKey{
		p("edffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f"), // y = p
		p("eeffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f"), // y = p+1, the identity
		p("0100000000000000000000000000000000000000000000000000000000000080"), // x = -0
		p("0200000000000000000000000000000000000000000000000000000000000000"), // y = 2: no x on the curve
	} {
		if _, err := parseClientKey(edLine(t, key)); !errors.Is(err, ErrKey) {
			t.Fatalf("key %x accepted: %v", key, err)
		}
	}
}

func TestClientKeyOfFullOrderIsAccepted(t *testing.T) {
	for range 16 {
		pub, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseClientKey(edLine(t, pub)); err != nil {
			t.Fatal(err)
		}
		if p, _ := decodeEd25519(pub); p.encode().Equal(pub) != true {
			t.Fatal("decode and encode disagree")
		}
	}
}
