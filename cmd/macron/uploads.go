package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"path"
	"strings"
	"sync"

	"github.com/Metixpro/macron-agent/pkg/telegram"
)

func (b *Bot) incomingDocument(ctx context.Context, session string, m *telegram.Message) (string, int64, error) {
	v, _ := b.uploadLocks.LoadOrStore(session, &sync.Mutex{})
	lock := v.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	d := m.Document
	sum := sha256.Sum256([]byte(d.FileID))
	marker := fmt.Sprintf(".incoming/%s-%x.json", msgTag(m), sum[:8])
	var record struct {
		Name string `json:"name"`
	}
	if data, _, err := b.app.Machine.ReadFile(session, marker, 4096); err == nil && json.Unmarshal(data, &record) == nil && record.Name != "" && path.Base(record.Name) == record.Name {
		if st, err := b.app.Machine.Stat(session, record.Name); err == nil && st.Mode().IsRegular() && st.Size() > 0 {
			return record.Name, st.Size(), nil
		}
	}
	fallback := "file_" + msgTag(m)
	if d.FileName == "" && d.MimeType == "application/vnd.android.package-archive" {
		fallback += ".apk"
	}
	name := safeName(d.FileName, fallback)
	if _, err := b.app.Machine.Stat(session, name); err == nil {
		ext := path.Ext(name)
		stem := strings.TrimSuffix(name, ext)
		name = stem + "_" + msgTag(m) + ext
		for n := 1; ; n++ {
			if _, err := b.app.Machine.Stat(session, name); err != nil {
				break
			}
			name = fmt.Sprintf("%s_%s_%x_%d%s", stem, msgTag(m), sum[:4], n, ext)
		}
	}
	size, err := b.saveIncoming(ctx, session, d.FileID, name, d.FileSize)
	if err != nil {
		return "", 0, err
	}
	record.Name = name
	data, _ := json.Marshal(record)
	if err := b.app.Machine.WriteFile(session, marker, data); err != nil {
		log.Printf("incoming document index: %v", err)
	}
	return name, size, nil
}
