package main

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/telegram"
)

func albumItem(id int64, group, caption, fileID string) *telegram.Message {
	m := private(12, "")
	m.MessageID, m.MediaGroupID, m.Caption = id, group, caption
	m.Photo = []telegram.PhotoSize{{FileID: fileID, Width: 800, Height: 600}}
	return m
}

func TestMergeAlbumLeadsWithTheCaptionedItem(t *testing.T) {
	a, b, c := albumItem(12, "g", "", "f12"), albumItem(11, "g", "", "f11"), albumItem(13, "g", "این سایتو میشناسید", "f13")
	b.ReplyToMessage = &telegram.Message{MessageID: 3, Text: "قبلی"}
	m := mergeAlbum([]*telegram.Message{a, b, c})
	if m.MessageID != 13 || m.Caption != "این سایتو میشناسید" || !m.AlbumDone {
		t.Fatalf("lead: %+v", m)
	}
	if len(m.Album) != 2 || m.Album[0].MessageID != 11 || m.Album[1].MessageID != 12 {
		t.Errorf("the others, in the order sent: %v", m.Album)
	}
	if m.ReplyToMessage == nil || m.ReplyToMessage.MessageID != 3 {
		t.Error("a reply on another item belongs to the album")
	}
	if a.AlbumDone || len(a.Album) != 0 {
		t.Error("the items themselves are not changed")
	}

	if m := mergeAlbum([]*telegram.Message{albumItem(5, "g", "", "x"), albumItem(4, "g", "", "y")}); m.MessageID != 4 || len(m.Album) != 1 {
		t.Errorf("no caption: %+v", m)
	}
}

func TestAlbumCollectorWaitsForTheLastItem(t *testing.T) {
	oldQ, oldM := albumQuiet, albumMax
	albumQuiet, albumMax = 80*time.Millisecond, 2*time.Second
	t.Cleanup(func() { albumQuiet, albumMax = oldQ, oldM })
	var c albumCollector
	var wg sync.WaitGroup
	var got [][]int64
	var lock sync.Mutex
	fire := func(items []*telegram.Message) {
		var ids []int64
		for _, it := range items {
			ids = append(ids, it.MessageID)
		}
		lock.Lock()
		got = append(got, ids)
		lock.Unlock()
	}
	c.add(albumItem(1, "A", "", "a"), &wg, fire)
	c.add(albumItem(10, "B", "", "b"), &wg, fire)
	time.Sleep(30 * time.Millisecond)
	c.add(albumItem(2, "A", "", "a"), &wg, fire)
	time.Sleep(30 * time.Millisecond)
	c.add(albumItem(3, "A", "", "a"), &wg, fire)
	wg.Wait()
	lock.Lock()
	first := append([][]int64(nil), got...)
	lock.Unlock()
	if len(first) != 2 {
		t.Fatalf("two albums: %v", first)
	}
	sizes := map[int]bool{len(first[0]): true, len(first[1]): true}
	if !sizes[3] || !sizes[1] {
		t.Errorf("one of three items and one of one: %v", first)
	}

	start := time.Now()
	for i := range albumItems {
		c.add(albumItem(int64(100+i), "full", "", "z"), &wg, fire)
	}
	wg.Wait()
	if time.Since(start) > 70*time.Millisecond {
		t.Errorf("a full album is complete at once: %v", time.Since(start))
	}
}

func TestAnAlbumIsOneRequest(t *testing.T) {
	h := newHarness(t)
	oldQ := albumQuiet
	albumQuiet = 60 * time.Millisecond
	t.Cleanup(func() { albumQuiet = oldQ })
	h.tg.files["a1"], h.tg.files["a2"] = testJPEG(800, 600), testJPEG(640, 480)
	h.llm.reply = say("آره، می‌شناسمش.")

	first := albumItem(70, "grp1", "", "a1")
	second := albumItem(71, "grp1", "این سایتو میشناسید", "a2")
	h.bot.handleMessage(first)
	h.bot.handleMessage(second)
	h.bot.wg.Wait()

	if n := len(h.llm.requests); n != 1 {
		t.Fatalf("an album is one request, got %d", n)
	}
	req := llmText(h.llm.last())
	for _, want := range []string{"این سایتو میشناسید", "photo_70.jpg", "photo_71.jpg", "2 files together as one album"} {
		if !strings.Contains(req, want) {
			t.Errorf("the model request misses %q:\n%s", want, req)
		}
	}
	if strings.Count(req, "data:image/jpeg;base64") != 2 {
		t.Errorf("both photos are visible to the model")
	}
	for _, name := range []string{"photo_70.jpg", "photo_71.jpg"} {
		if _, err := h.bot.app.Machine.Stat(sessionFor(12), name); err != nil {
			t.Errorf("%s is saved in the workspace", name)
		}
	}
	h.wantText("آره، می‌شناسمش.")
	h.noText("هنوز دارم روی درخواست قبلی")
	mem, _ := h.bot.mem.Load(bg, 12)
	if len(mem.History) == 0 || !strings.Contains(mem.History[0].Content, "photo_70.jpg") || !strings.Contains(mem.History[0].Content, "photo_71.jpg") {
		t.Errorf("memory of the turn: %v", mem.History)
	}

	h.tg.reset()
	h.llm.requests = nil
	h.bot.handleMessage(albumItem(80, "grp2", "", "a1"))
	h.bot.handleMessage(albumItem(81, "grp2", "", "a2"))
	h.bot.wg.Wait()
	if n := len(h.llm.requests); n != 1 {
		t.Errorf("an album with no caption is one request too, got %d", n)
	}
	if req := llmText(h.llm.last()); !strings.Contains(req, "(sent an album of 2 files)") && !strings.Contains(req, "(یه آلبوم با 2 فایل فرستاد)") {
		t.Errorf("the placeholder of an album with no words:\n%s", req)
	}
}
