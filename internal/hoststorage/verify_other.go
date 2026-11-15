//go:build !linux

package hoststorage

import (
	"context"
	"errors"
	"fmt"
	"syscall"
)

func (Policy) VerifyStorage(context.Context, string) (StorageIdentity, error) {
	return StorageIdentity{}, errors.New("managed storage verification requires Linux")
}

func unmanagedStorageMount(root string) (string, error) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(root, &fs); err != nil {
		return "", err
	}
	return fmt.Sprintf("native:%v:%s:%s", fs.Fsid, storageNativeString(fs.Mntonname[:]), storageNativeString(fs.Mntfromname[:])), nil
}

func storageNativeString(raw []int8) string {
	var b []byte
	for _, c := range raw {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b)
}
