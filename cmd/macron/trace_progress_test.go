package main

import (
	"strings"
	"testing"
	"time"
)

func TestTheTerminalShowsLiveProgress(t *testing.T) {
	var tr trace
	now := time.Now()
	tr.start(0, "download_file", now)
	tr.progress(0, "download_file", "45% · 12.3/27.0 MB", false)
	tr.start(0, "run_code", now)
	tr.progress(0, "run_code", "epoch 3/10 · loss 0.412 ```", true)
	out := tr.render(now.Add(5*time.Second), now, true)
	if !strings.Contains(out, "Download") || !strings.Contains(out, "· 45% · 12.3/27.0 MB") {
		t.Errorf("percent and size next to the download:\n%s", out)
	}
	if !strings.Contains(out, "↳ epoch 3/10 · loss 0.412") || strings.Contains(out, "```") {
		t.Errorf("the program's last line under its step, never closing the code block:\n%s", out)
	}
	tr.end(0, "run_code", false)
	tr.end(0, "download_file", false)
	if out := tr.render(now.Add(6*time.Second), now, true); strings.Contains(out, "epoch") || strings.Contains(out, "45%") {
		t.Errorf("finished steps drop their progress:\n%s", out)
	}
}

func (t *trace) render(now, started time.Time, animate bool) string {
	title, body := t.card(now, started, animate)
	return title + "\n" + body
}
