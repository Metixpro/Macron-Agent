//go:build unix

package diskfree

import "golang.org/x/sys/unix"

func Space(path string) (total, free uint64, ok bool) {
	var st unix.Statfs_t
	if unix.Statfs(path, &st) != nil {
		return 0, 0, false
	}
	return st.Blocks * uint64(st.Bsize), st.Bavail * uint64(st.Bsize), true
}

func SameDevice(a, b string) bool {
	var x, y unix.Stat_t
	return unix.Stat(a, &x) == nil && unix.Stat(b, &y) == nil && x.Dev == y.Dev
}
