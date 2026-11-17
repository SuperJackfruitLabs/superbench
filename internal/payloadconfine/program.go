// Package payloadconfine builds and installs the seccomp filter every process
// running as the payload account inherits. A file's owner may change its XFS
// project ID and clear the project-inherit flag from the initial user
// namespace without any capability, so project quotas alone do not hold
// against the payload. The filter refuses those operations instead.
package payloadconfine

import "encoding/binary"

// Instruction is the kernel's struct sock_filter, field for field.
type Instruction struct {
	Code   uint16
	Jt, Jf uint8
	K      uint32
}

// Arch names one supported seccomp architecture.
type Arch struct {
	Audit uint32 // AUDIT_ARCH_* the kernel reports in seccomp_data.arch
	Ioctl uint32 // native ioctl syscall number
	X32   bool   // the x32 ABI shares Audit; its numbers carry bit 30
}

var (
	AMD64 = Arch{Audit: 0xc000003e, Ioctl: 16, X32: true}
	ARM64 = Arch{Audit: 0xc00000b7, Ioctl: 29}
)

const (
	ldAbs = 0x20 // BPF_LD | BPF_W | BPF_ABS
	jeqK  = 0x15 // BPF_JMP | BPF_JEQ | BPF_K
	jgeK  = 0x35 // BPF_JMP | BPF_JGE | BPF_K
	retK  = 0x06 // BPF_RET | BPF_K

	RetKillProcess = 0x80000000
	RetErrnoEPERM  = 0x00050000 | 1
	RetAllow       = 0x7fff0000

	offNr    = 0
	offArch  = 4
	offArg1  = 24 // low 32 bits of args[1] on little-endian amd64 and arm64
	x32Bit   = 0x40000000
	maxDepth = 255
)

// Denied syscalls share their numbers on amd64 and arm64.
var DeniedSyscalls = []uint32{
	425, // io_uring_setup
	426, // io_uring_enter
	427, // io_uring_register
	469, // file_setattr: sets the same fsxattr, project ID included, by path
}

// DeniedIoctls are compared against the low 32 bits of the request, which is
// all the kernel's unsigned int cmd keeps.
var DeniedIoctls = []uint32{
	0x401c5820, // FS_IOC_FSSETXATTR (= XFS_IOC_FSSETXATTR)
	0x40086602, // FS_IOC_SETFLAGS (= XFS_IOC_SETXFLAGS, EXT4_IOC_SETFLAGS)
	0x40046602, // FS_IOC32_SETFLAGS
}

// Program returns the filter for a. An unexpected architecture, and on amd64
// the x32 ABI, kills the process; denied calls fail with EPERM.
func Program(a Arch) []Instruction {
	type jump struct {
		at     int
		target string
		onTrue bool
	}
	var prog []Instruction
	var jumps []jump
	emit := func(code uint16, k uint32) int {
		prog = append(prog, Instruction{Code: code, K: k})
		return len(prog) - 1
	}
	branch := func(code uint16, k uint32, onTrue, onFalse string) {
		at := emit(code, k)
		if onTrue != "" {
			jumps = append(jumps, jump{at, onTrue, true})
		}
		if onFalse != "" {
			jumps = append(jumps, jump{at, onFalse, false})
		}
	}
	emit(ldAbs, offArch)
	branch(jeqK, a.Audit, "", "kill")
	emit(ldAbs, offNr)
	if a.X32 {
		branch(jgeK, x32Bit, "kill", "")
	}
	for _, nr := range DeniedSyscalls {
		branch(jeqK, nr, "deny", "")
	}
	branch(jeqK, a.Ioctl, "", "allow")
	emit(ldAbs, offArg1)
	for _, cmd := range DeniedIoctls {
		branch(jeqK, cmd, "deny", "")
	}
	labels := map[string]int{"allow": emit(retK, RetAllow), "deny": emit(retK, RetErrnoEPERM), "kill": emit(retK, RetKillProcess)}
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

// Encode returns the exact bytes the kernel receives.
func Encode(prog []Instruction) []byte {
	raw := make([]byte, 0, 8*len(prog))
	for _, i := range prog {
		raw = binary.LittleEndian.AppendUint16(raw, i.Code)
		raw = append(raw, i.Jt, i.Jf)
		raw = binary.LittleEndian.AppendUint32(raw, i.K)
	}
	return raw
}
