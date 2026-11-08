package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTranscriptions(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is needed")
	}
	dir := t.TempDir()
	cli := filepath.Join(dir, "whisper-cli")
	// A stand-in for whisper.cpp: writes its arguments as the transcript.
	_ = os.WriteFile(cli, []byte("#!/bin/sh\nof=\"\"; prev=\"\"; for a in \"$@\"; do [ \"$prev\" = \"-of\" ] && of=\"$a\"; prev=\"$a\"; done\n"+
		"printf ' سلام دنیا\\n\\n lang=%s\\n' \"$(echo \"$@\" | sed 's/.*-l \\([a-z]*\\).*/\\1/')\" > \"$of.txt\"\n"), 0o755)
	audio := filepath.Join(dir, "a.ogg")
	if out, err := exec.Command(ffmpeg, "-y", "-v", "error", "-f", "lavfi", "-i", "sine=duration=1", "-c:a", "libvorbis", audio).CombinedOutput(); err != nil {
		t.Skipf("cannot make a recording: %v %s", err, out)
	}
	s := &server{model: "m.bin", cli: cli, ffmpeg: ffmpeg, name: "whisper-large-v3-turbo", threads: 2, slot: make(chan struct{}, 1)}

	post := func(lang string) *httptest.ResponseRecorder {
		var body bytes.Buffer
		w := multipart.NewWriter(&body)
		_ = w.WriteField("model", "whisper-large-v3-turbo")
		if lang != "" {
			_ = w.WriteField("language", lang)
		}
		fw, _ := w.CreateFormFile("file", "voice.ogg")
		data, _ := os.ReadFile(audio)
		fw.Write(data)
		w.Close()
		req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", &body)
		req.Header.Set("Content-Type", w.FormDataContentType())
		rec := httptest.NewRecorder()
		s.transcribe(rec, req)
		return rec
	}
	rec := post("fa")
	var out struct{ Text string }
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Text != "سلام دنیا lang=fa" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := post(""); !strings.Contains(rec.Body.String(), "lang=auto") {
		t.Errorf("no language: auto: %s", rec.Body)
	}
	if rec := post("persian"); rec.Code != 400 {
		t.Errorf("a bad language: %d", rec.Code)
	}
}
