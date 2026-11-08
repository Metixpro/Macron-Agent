package main

import (
	"path/filepath"
	"strings"

	"github.com/Metixpro/macron-agent/pkg/tools"
)

func chosenFiles(files *fileTracker, box *outbox, allow *tools.Allowance) ([]string, int) {
	return capPictures(files.deliveries(box.list()), allow.Images)
}

func capPictures(files []string, n int) ([]string, int) {
	if n <= 0 {
		return files, 0
	}
	var out []string
	pictures, held := 0, 0
	for _, f := range files {
		if imageExt[strings.ToLower(filepath.Ext(f))] {
			if pictures == n {
				held++
				continue
			}
			pictures++
		}
		out = append(out, f)
	}
	return out, held
}
