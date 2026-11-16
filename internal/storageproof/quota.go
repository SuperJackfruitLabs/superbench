package storageproof

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

func ParseManagedMount(raw []byte, root string, device uint64) (string, string, error) {
	f := strings.Fields(string(raw))
	major := (device>>8)&0xfff | (device>>32)&0xfffff000
	minor := device&0xff | (device>>12)&0xffffff00
	if len(f) != 5 || !volumePattern.MatchString(f[1]) || f[2] != "/" || f[3] != root || f[4] != fmt.Sprintf("%d:%d", major, minor) {
		return "", "", errors.New("expected exact dedicated filesystem mount identity")
	}
	if id, err := strconv.ParseUint(f[0], 10, 64); err != nil || id == 0 {
		return "", "", errors.New("invalid mount ID")
	}
	return strings.Join(f, " "), strings.ToLower(f[1]), nil
}

func VerifyQuota(raw []byte, id uint32, want uint64) error {
	count := 0
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[0] != "#"+strconv.FormatUint(uint64(id), 10) {
			continue
		}
		count++
		hard, err := strconv.ParseUint(f[3], 10, 64)
		if err != nil || hard != want {
			return errors.New("kernel project cap differs from ledger")
		}
	}
	if count != 1 {
		return errors.New("kernel project quota could not be uniquely verified")
	}
	return nil
}

func VerifyUnused(raw []byte, id uint32) error {
	count := 0
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[0] != "#"+strconv.FormatUint(uint64(id), 10) {
			continue
		}
		count++
		used, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil || used != 0 {
			return errors.New("project retains blocks or inodes; reservation retained")
		}
	}
	if count != 1 {
		return errors.New("project usage cannot be uniquely verified")
	}
	return nil
}
