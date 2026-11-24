package payloadconfine

import (
	"encoding/binary"
	"testing"
)

func runSession(t *testing.T, prog []Instruction, data [64]byte) uint32 {
	t.Helper()
	var acc uint32
	for pc := 0; pc < len(prog); pc++ {
		i := prog[pc]
		switch i.Code {
		case ldAbs:
			acc = binary.LittleEndian.Uint32(data[i.K:])
		case jeqK, jgeK, jsetK:
			taken := i.Code == jeqK && acc == i.K || i.Code == jgeK && acc >= i.K || i.Code == jsetK && acc&i.K != 0
			if taken {
				pc += int(i.Jt)
			} else {
				pc += int(i.Jf)
			}
		case retK:
			return i.K
		default:
			t.Fatalf("unexpected opcode %#x", i.Code)
		}
	}
	t.Fatal("program fell off its end")
	return 0
}

func TestSessionProgramRefusesUserNamespaces(t *testing.T) {
	const newUser, newNS, thread = 0x10000000, 0x00020000, 0x00010000
	for _, a := range []Arch{AMD64, ARM64} {
		n := sessionNumbers[a.Audit]
		prog := SessionProgram(a)
		for name, c := range map[string]struct {
			data [64]byte
			want uint32
		}{
			"clone thread":      {call(a.Audit, n.clone, thread), RetAllow},
			"clone fork":        {call(a.Audit, n.clone, 17), RetAllow},
			"clone new user":    {call(a.Audit, n.clone, newUser|17), RetErrnoEPERM},
			"unshare new ns":    {call(a.Audit, n.unshare, newNS), RetAllow},
			"unshare new user":  {call(a.Audit, n.unshare, newUser|newNS), RetErrnoEPERM},
			"unshare high bits": {call(a.Audit, n.unshare, 1<<40|newUser), RetErrnoEPERM},
			"setns":             {call(a.Audit, n.setns, 3, 0), RetErrnoEPERM},
			"clone3":            {call(a.Audit, n.clone3, 0, 88), RetErrnoENOSYS},
			"read":              {call(a.Audit, 0, 0), RetAllow},
			"foreign arch":      {call(0x40000003, n.unshare, newUser), RetKillProcess},
		} {
			if got := runSession(t, prog, c.data); got != c.want {
				t.Errorf("%#x %s: %#x, want %#x", a.Audit, name, got, c.want)
			}
		}
	}
	if got := runSession(t, SessionProgram(AMD64), call(AMD64.Audit, x32Bit|272, 0x10000000)); got != RetKillProcess {
		t.Errorf("x32 unshare: %#x", got)
	}
	if SessionProgram(Arch{Audit: 1}) != nil {
		t.Error("unknown architecture got a session filter")
	}
}
