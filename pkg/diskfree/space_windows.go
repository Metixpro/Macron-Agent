package diskfree

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func Space(path string) (total, free uint64, ok bool) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, false
	}
	var avail, all, totalFree uint64
	if windows.GetDiskFreeSpaceEx(p, &avail, &all, &totalFree) != nil {
		return 0, 0, false
	}
	return all, avail, true
}

func SameDevice(a, b string) bool {
	return strings.EqualFold(filepath.VolumeName(a), filepath.VolumeName(b))
}
