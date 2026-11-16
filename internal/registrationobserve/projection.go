package registrationobserve

import (
	"strconv"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
)

// Projection copies only the explicit private observation fields. It does not
// authorize a caller or certify a Result constructed outside Observe.
func Projection(result Result) *protocolv2.RegistrationObservation {
	l := result.Layout
	node := func(n payloadexec.Node) protocolv2.RegistrationNode {
		return protocolv2.RegistrationNode{Device: strconv.FormatUint(n.Device, 10), Inode: strconv.FormatUint(n.Inode, 10)}
	}
	v := &protocolv2.RegistrationObservation{Key: l.Key, BenchID: l.ID, RootPath: result.RootPath, LayoutVersion: l.Version, PolicySHA256: l.PolicyDigest, Root: node(l.Root), RootHome: node(l.RootHome), RootTemp: node(l.RootTemp)}
	for _, m := range l.Members {
		v.Members = append(v.Members, protocolv2.RegistrationMember{Name: m.Name, Checkout: node(m.Checkout), Admin: node(m.Admin), Home: node(m.Home), Temp: node(m.Temp)})
	}
	cp := result.Checkpoint
	v.Checkpoint = protocolv2.RegistrationCheckpoint{GenerationID: cp.GenerationID, ManifestSHA256: cp.Digest, Seal: cp.Seal}
	for _, m := range cp.Members {
		v.Checkpoint.Members = append(v.Checkpoint.Members, protocolv2.Commit{Name: m.Name, Commit: m.Commit})
	}
	return v
}
