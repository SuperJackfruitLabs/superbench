package payloadexec

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"unicode/utf8"
)

const LayoutVersion = "superbench.payload-layout.v1"

// Thirty-two names of at most 80 ASCII bytes and five pairs of uint64 device /
// inode identifiers per member fit below this private metadata envelope bound.
const MaxLayout = 64 << 10

type Node struct{ Device, Inode uint64 }
type LayoutMember struct {
	Name                        string
	Checkout, Admin, Home, Temp Node
}

// Layout is a completed daemon-private registration, never a caller assertion
// or permission-conversion plan. Its paths are derived from fixed policy + key;
// it cannot select arbitrary root, checkout, admin, HOME or temporary paths.
type Layout struct {
	Version, Key, ID, PolicyDigest string
	Scalar                         bool
	Root                           Node
	RootHome, RootTemp             Node
	Members                        []LayoutMember
}

func policyDigest(p Policy) (string, error) {
	raw, err := p.Bytes()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func (l Layout) Validate(p Policy) error {
	digest, err := policyDigest(p)
	if err != nil || l.Version != LayoutVersion || !logical.MatchString(l.Key) || !identity.MatchString(l.ID) || l.PolicyDigest != digest || l.Root.Inode == 0 || len(l.Members) == 0 || len(l.Members) > 32 || l.Scalar && len(l.Members) != 1 {
		return ErrPolicy
	}
	last := ""
	seen := map[Node]bool{l.Root: true}
	if l.Scalar {
		if l.RootHome != (Node{}) || l.RootTemp != (Node{}) {
			return ErrPolicy
		}
	} else {
		for _, node := range []Node{l.RootHome, l.RootTemp} {
			if node.Inode == 0 || node.Device != l.Root.Device || seen[node] {
				return ErrPolicy
			}
			seen[node] = true
		}
	}
	for i, member := range l.Members {
		if l.Scalar && member.Name != "" || !l.Scalar && (!logical.MatchString(member.Name) || i > 0 && member.Name <= last) {
			return ErrPolicy
		}
		for _, node := range []Node{member.Checkout, member.Admin, member.Home, member.Temp} {
			if node.Inode == 0 || node.Device != l.Root.Device || seen[node] {
				return ErrPolicy
			}
			seen[node] = true
		}
		last = member.Name
	}
	return nil
}

func (l Layout) Bytes(p Policy) ([]byte, error) {
	if err := l.Validate(p); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(l)
	if err != nil || len(raw) > MaxLayout {
		return nil, ErrPolicy
	}
	return raw, nil
}

func ParseLayout(p Policy, raw []byte) (Layout, error) {
	var l Layout
	if len(raw) > MaxLayout || !utf8.Valid(raw) || json.Unmarshal(raw, &l) != nil {
		return Layout{}, ErrPolicy
	}
	canonical, err := l.Bytes(p)
	var compact bytes.Buffer
	if err != nil || json.Compact(&compact, raw) != nil || !bytes.Equal(compact.Bytes(), canonical) {
		return Layout{}, ErrPolicy
	}
	return l, nil
}

func (l Layout) paths(p Policy, name string) (checkout, admin, home, tmp string, err error) {
	if err := l.Validate(p); err != nil {
		return "", "", "", "", err
	}
	root := filepath.Join(p.DataRoot, "benches", l.Key)
	if !l.Scalar && name == "" {
		return root, "", filepath.Join(root, ".superbench-runtime", ".root", "home"), filepath.Join(root, ".superbench-runtime", ".root", "tmp"), nil
	}
	found := false
	for _, member := range l.Members {
		found = found || member.Name == name
	}
	if !found {
		return "", "", "", "", ErrPolicy
	}
	part := name
	if l.Scalar {
		part = "worktree"
	}
	return filepath.Join(root, part), filepath.Join(root, ".superbench-git", part), filepath.Join(root, ".superbench-runtime", part, "home"), filepath.Join(root, ".superbench-runtime", part, "tmp"), nil
}
