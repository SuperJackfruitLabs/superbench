package payloadconfine

// Remote sessions stack a second filter on top of Program: the session must
// not create a user namespace, in which its inherited mounts could be
// unlocked from a fresh owner, nor join any namespace. clone3 fails with
// ENOSYS (its flags sit behind a pointer the filter cannot read), so libc
// falls back to clone, whose flags the filter checks.
const (
	jsetK          = 0x45 // BPF_JMP | BPF_JSET | BPF_K
	offArg0        = 16   // low 32 bits of args[0]
	cloneNewUser   = 0x10000000
	RetErrnoENOSYS = 0x00050000 | 38
)

// sessionCalls are the per-architecture numbers the session filter checks.
type sessionCalls struct{ clone, unshare, setns, clone3 uint32 }

var sessionNumbers = map[uint32]sessionCalls{
	AMD64.Audit: {clone: 56, unshare: 272, setns: 308, clone3: 435},
	ARM64.Audit: {clone: 220, unshare: 97, setns: 268, clone3: 435},
}

// SessionProgram returns the session filter for a, or nil for an
// architecture without reviewed numbers. An unexpected architecture, and on
// amd64 the x32 ABI, kills the process, as in Program.
func SessionProgram(a Arch) []Instruction {
	n, ok := sessionNumbers[a.Audit]
	if !ok {
		return nil
	}
	const allow, deny, enosys, kill, flags, unflagged = 100, 101, 102, 103, 104, 105
	var prog []Instruction
	type jump struct {
		at, target int
		onTrue     bool
	}
	var jumps []jump
	emit := func(code uint16, k uint32) int {
		prog = append(prog, Instruction{Code: code, K: k})
		return len(prog) - 1
	}
	branch := func(code uint16, k uint32, onTrue, onFalse int) {
		at := emit(code, k)
		if onTrue != 0 {
			jumps = append(jumps, jump{at, onTrue, true})
		}
		if onFalse != 0 {
			jumps = append(jumps, jump{at, onFalse, false})
		}
	}
	emit(ldAbs, offArch)
	branch(jeqK, a.Audit, 0, kill)
	emit(ldAbs, offNr)
	if a.X32 {
		branch(jeqK, skippedNr, allow, 0)
		branch(jgeK, x32Bit, kill, 0)
	}
	branch(jeqK, n.clone, flags, 0)
	branch(jeqK, n.unshare, flags, 0)
	branch(jeqK, n.setns, deny, 0)
	branch(jeqK, n.clone3, enosys, 0)
	labels := map[int]int{allow: emit(retK, RetAllow)}
	labels[flags] = emit(ldAbs, offArg0)
	branch(jsetK, cloneNewUser, deny, unflagged)
	labels[unflagged] = emit(retK, RetAllow)
	labels[deny] = emit(retK, RetErrnoEPERM)
	labels[enosys] = emit(retK, RetErrnoENOSYS)
	labels[kill] = emit(retK, RetKillProcess)
	for _, j := range jumps {
		offset := labels[j.target] - j.at - 1
		if offset < 0 || offset > maxDepth {
			panic("payloadconfine: jump out of range")
		}
		if j.onTrue {
			prog[j.at].Jt = uint8(offset)
		} else {
			prog[j.at].Jf = uint8(offset)
		}
	}
	return prog
}
