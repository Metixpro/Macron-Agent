package main

import (
	"path/filepath"
	"strings"

	"github.com/Metixpro/macron-agent/pkg/tools"
)

// chosenFiles are the files the model chose (send_file) that still exist,
// within the request's pictures, and how many pictures were held back.
func chosenFiles(files *fileTracker, box *outbox, allow *tools.Allowance) ([]string, int) {
	return capPictures(files.deliveries(box.list()), allow.Images)
}

// capPictures keeps at most n pictures among the files of an answer (the
// rest stay in the workspace); it returns how many were held back.
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
