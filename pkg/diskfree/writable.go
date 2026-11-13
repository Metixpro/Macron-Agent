package diskfree

import "os"

func Writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".macron-write-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	return os.Remove(name) == nil
}
