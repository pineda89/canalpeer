//go:build !windows

package mobile

import (
	"errors"

	"golang.org/x/sys/unix"
)

func freeBytes(dir string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

func diskFull(err error) bool { return errors.Is(err, unix.ENOSPC) }
