package payloadconfine

import (
	"encoding/binary"
	"encoding/hex"
	"testing"
)

// run is a minimal classic-BPF interpreter for the four opcodes the filter uses.
func run(t *testing.T, prog []Instruction, data [64]byte) uint32 {
	t.Helper()
	var acc uint32
	for pc := 0; pc < len(prog); pc++ {
		i := prog[pc]
		switch i.Code {
		case ldAbs:
			acc = binary.LittleEndian.Uint32(data[i.K:])
		case jeqK, jgeK:
			taken := acc == i.K || i.Code == jgeK && acc >= i.K
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

func call(arch, nr uint32, args ...uint64) [64]byte {
	var d [64]byte
	binary.LittleEndian.PutUint32(d[0:], nr)
	binary.LittleEndian.PutUint32(d[4:], arch)
	for n, a := range args {
		binary.LittleEndian.PutUint64(d[16+8*n:], a)
	}
	return d
}

func TestProgramRefusesQuotaProjectRoutes(t *testing.T) {
	for _, a := range []Arch{AMD64, ARM64} {
		prog := Program(a)
		// FS_IOC_FSSETXATTR, FS_IOC_SETFLAGS, FS_IOC32_SETFLAGS, written out so a
		// shortened list cannot pass.
		for _, cmd := range []uint32{0x401c5820, 0x40086602, 0x40046602} {
			if got := run(t, prog, call(a.Audit, a.Ioctl, 3, uint64(cmd))); got != RetErrnoEPERM {
				t.Fatalf("%#x ioctl %#x: %#x", a.Audit, cmd, got)
			}
			// The kernel truncates cmd to 32 bits: high bits must not bypass.
			if got := run(t, prog, call(a.Audit, a.Ioctl, 3, 0xffffffff00000000|uint64(cmd))); got != RetErrnoEPERM {
				t.Fatalf("%#x high-bit ioctl %#x: %#x", a.Audit, cmd, got)
			}
		}
		for _, nr := range []uint32{425, 426, 427, 469} { // io_uring_setup/enter/register, file_setattr
			if got := run(t, prog, call(a.Audit, nr)); got != RetErrnoEPERM {
				t.Fatalf("%#x syscall %d: %#x", a.Audit, nr, got)
			}
		}
		// FS_IOC_FSGETXATTR, FS_IOC_GETFLAGS, TCGETS and an ordinary read stay allowed.
		for _, cmd := range []uint64{0x801c581f, 0x80086601, 0x5401} {
			if got := run(t, prog, call(a.Audit, a.Ioctl, 3, cmd)); got != RetAllow {
				t.Fatalf("%#x allowed ioctl %#x: %#x", a.Audit, cmd, got)
			}
		}
		if got := run(t, prog, call(a.Audit, 0, 0, 0x401c5820)); got != RetAllow {
			t.Fatalf("%#x syscall 0 with a denied-looking argument: %#x", a.Audit, got)
		}
		// Any other architecture, including the compat 32-bit ABIs, is killed.
		for _, other := range []uint32{0x40000003, 0x40000028, AMD64.Audit ^ ARM64.Audit ^ a.Audit} {
			if got := run(t, prog, call(other, a.Ioctl, 3, 0x401c5820)); got != RetKillProcess {
				t.Fatalf("%#x under %#x: %#x", other, a.Audit, got)
			}
		}
	}
	// x32 shares AUDIT_ARCH_X86_64 and sets bit 30 of the number.
	if got := run(t, Program(AMD64), call(AMD64.Audit, 0x40000000|514, 3, 0x401c5820)); got != RetKillProcess {
		t.Fatalf("x32 ioctl: %#x", got)
	}
}

// The exact bytes are pinned: any change to the filter is a reviewed change.
func TestProgramBytes(t *testing.T) {
	for _, c := range []struct {
		a    Arch
		want string
	}{
		{AMD64, amd64Bytes},
		{ARM64, arm64Bytes},
	} {
		if got := hex.EncodeToString(Encode(Program(c.a))); got != c.want {
			t.Fatalf("%#x program bytes:\n got %s\nwant %s", c.a.Audit, got, c.want)
		}
	}
}

// Instruction order: arch check, number load, [x32 check], denied syscalls,
// ioctl test, request load, denied requests, then allow, EPERM and kill.
const (
	amd64Bytes = "20000000040000001500000d3e0000c0200000000000000035000b000000004015000900a901000015000800aa01000015000700ab01000015000600d5010000150000041000000020000000180000001500030020581c4015000200026608401500010002660440060000000000ff7f06000000010005000600000000000080"
	arm64Bytes = "20000000040000001500000cb70000c0200000000000000015000900a901000015000800aa01000015000700ab01000015000600d5010000150000041d00000020000000180000001500030020581c4015000200026608401500010002660440060000000000ff7f06000000010005000600000000000080"
)
