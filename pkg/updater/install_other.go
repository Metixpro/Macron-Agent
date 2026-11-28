//go:build !windows

package updater

import (
	"context"
	"os"
)

func installBinaries(ctx context.Context, self string, bin, _ []byte, version string, report func(string, string, int)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	report("preflight", "Verifying new binary", 93)
	return Swap(self, bin, version)
}

func swapBinary(self, next string) error {
	old := self + ".old"
	_ = os.Remove(old)
	if err := os.Link(self, old); err != nil {
		if data, readErr := os.ReadFile(self); readErr == nil {
			_ = os.WriteFile(old, data, 0o755)
		}
	}
	return os.Rename(next, self)
}
