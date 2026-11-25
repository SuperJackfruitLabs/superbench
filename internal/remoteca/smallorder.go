package remoteca

import (
	"crypto/ed25519"
	"math/big"
)

// Curve25519 in its twisted Edwards form, -x² + y² = 1 + d·x²·y² over p.
var (
	edP     = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(19))
	edD     = edDiv(big.NewInt(-121665), big.NewInt(121666))
	edSqrt1 = new(big.Int).Exp(big.NewInt(2), new(big.Int).Rsh(new(big.Int).Sub(edP, big.NewInt(1)), 2), edP) // √-1
)

func edMod(x *big.Int) *big.Int { return x.Mod(x, edP) }
func edDiv(a, b *big.Int) *big.Int {
	return edMod(new(big.Int).Mul(a, new(big.Int).ModInverse(edMod(new(big.Int).Set(b)), edP)))
}

// edPoint is an affine point; the curve is complete, so add needs no cases.
type edPoint struct{ x, y *big.Int }

func (a edPoint) add(b edPoint) edPoint {
	xx := new(big.Int).Mul(a.x, b.x)
	yy := new(big.Int).Mul(a.y, b.y)
	t := edMod(new(big.Int).Mul(edD, new(big.Int).Mul(xx, yy)))
	x := new(big.Int).Add(new(big.Int).Mul(a.x, b.y), new(big.Int).Mul(a.y, b.x))
	y := new(big.Int).Add(yy, xx)
	return edPoint{
		x: edDiv(x, new(big.Int).Add(big.NewInt(1), t)),
		y: edDiv(y, new(big.Int).Sub(big.NewInt(1), t)),
	}
}

// decodeEd25519 is RFC 8032's point decoding. It refuses a y at or above p,
// a y with no x on the curve, and the negative zero x.
func decodeEd25519(key ed25519.PublicKey) (edPoint, bool) {
	if len(key) != ed25519.PublicKeySize {
		return edPoint{}, false
	}
	le := make([]byte, len(key))
	for i, b := range key {
		le[len(key)-1-i] = b
	}
	sign := le[0] >> 7
	le[0] &= 0x7f
	y := new(big.Int).SetBytes(le)
	if y.Cmp(edP) >= 0 {
		return edPoint{}, false
	}
	yy := new(big.Int).Mul(y, y)
	u := edMod(new(big.Int).Sub(yy, big.NewInt(1)))
	v := edMod(new(big.Int).Add(new(big.Int).Mul(edD, yy), big.NewInt(1)))
	w := edDiv(u, v)
	exp := new(big.Int).Rsh(new(big.Int).Add(edP, big.NewInt(3)), 3)
	x := new(big.Int).Exp(w, exp, edP)
	if edMod(new(big.Int).Mul(x, x)).Cmp(w) != 0 {
		x = edMod(x.Mul(x, edSqrt1))
	}
	if edMod(new(big.Int).Mul(x, x)).Cmp(w) != 0 {
		return edPoint{}, false
	}
	if x.Sign() == 0 && sign == 1 {
		return edPoint{}, false
	}
	if byte(x.Bit(0)) != sign {
		x.Sub(edP, x)
	}
	return edPoint{x: x, y: y}, true
}

// smallOrderEd25519 reports whether key is not a usable public key: it does
// not decode, or it lies in the eight-point torsion subgroup ([8]A is the
// identity). A signature under a small-order key is forgeable without any
// private key, so a certificate issued to one would bind no holder.
func smallOrderEd25519(key ed25519.PublicKey) bool {
	a, ok := decodeEd25519(key)
	if !ok {
		return true
	}
	for range 3 {
		a = a.add(a)
	}
	return a.x.Sign() == 0 && a.y.Cmp(big.NewInt(1)) == 0
}
