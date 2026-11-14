package quotapolicy

import "testing"

func TestRequestQuantities(t *testing.T) {
	if n, err := (Request{}).Bytes(); n != 0 || err != nil {
		t.Fatalf("unset quota: %d, %v", n, err)
	}
	for raw, want := range map[string]uint64{
		"1024": 1024, "1024B": 1024, "1K": 1 << 10, "3KiB": 3 << 10,
		"2M": 2 << 20, "2MiB": 2 << 20, "2G": 2 << 30, "2GiB": 2 << 30,
		"2T": 2 << 40, "2TiB": 2 << 40, "1P": 1 << 50, "1PiB": 1 << 50,
		"1125899906842624B": 1 << 50,
	} {
		t.Run(raw, func(t *testing.T) {
			if got, err := (Request{Size: &raw}).Bytes(); err != nil || got != want {
				t.Fatalf("got %d, %v; want %d", got, err, want)
			}
		})
	}
	for _, raw := range []string{
		"", "0", "0G", "1", "1023B", "1025B", "1.5G", "-1K", "+1K", "01K",
		" 1G", "1G ", "1g", "1KB", "1GB", "1e3K", "1 K", "1\nK", "1\x00K",
		"2P", "1125899906843648B", "18446744073709551615P", "18446744073709551616K",
	} {
		t.Run("invalid/"+raw, func(t *testing.T) {
			if got, err := (Request{Size: &raw}).Bytes(); err == nil || got != 0 {
				t.Fatalf("invalid quantity accepted: %d, %v", got, err)
			}
		})
	}
}

func TestValidBytesBounds(t *testing.T) {
	for _, n := range []uint64{0, 1024, 3 << 10, (1 << 50) - 1024, 1 << 50} {
		if !ValidBytes(n) {
			t.Fatalf("valid byte count rejected: %d", n)
		}
	}
	for _, n := range []uint64{1, 1023, 1025, (1 << 50) + 1024, ^uint64(0)} {
		if ValidBytes(n) {
			t.Fatalf("invalid byte count accepted: %d", n)
		}
	}
}
