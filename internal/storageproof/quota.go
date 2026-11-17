package storageproof

import (
	"errors"
	"strconv"
	"strings"
)

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
