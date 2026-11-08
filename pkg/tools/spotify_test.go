package tools

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	songID  = "4uLU6hMCjMI75M1A2tKUQC"
	albumID = "1DFixLWuPkv3KT3TnV35m3"
)

func fakeSpotify(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/track/") && r.Header.Get("User-Agent") != spotifyUA {
			http.Error(w, "the full facts are for link previews", http.StatusForbidden)
			return
		}
		page := func(entity string) {
			fmt.Fprintf(w, `<html><script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"state":{"data":{"entity":%s}}}}}</script></html>`, entity)
		}
		switch r.URL.Path {
		case "/embed/track/" + songID:
			page(`{"type":"track","name":"Song Name (feat. Guest)","artists":[{"name":"Singer"},{"name":"Guest"}],"duration":200000,
				"releaseDate":{"isoString":"2021-05-07T00:00:00Z"},"visualIdentity":{"image":[{"url":"` + srv.URL + `/small.jpg","maxWidth":64},
				{"url":"` + srv.URL + `/cover.jpg","maxWidth":640}]}}`)
		case "/track/" + songID:
			fmt.Fprint(w, `<meta property="og:title" content="Song Name"/><meta property="og:description" content="Singer, Guest · Best &amp; Last · Song · 2021"/>
				<meta name="music:album:track" content="3"/>`)
		case "/embed/album/" + albumID:
			page(`{"type":"album","name":"Best & Last","subtitle":"Singer","trackList":[
				{"uri":"spotify:track:0000000000000000000000","title":"Intro","subtitle":"Singer","duration":61000},
				{"uri":"spotify:track:` + songID + `","title":"Song Name (feat. Guest)","subtitle":"Singer,\u00a0Guest","duration":200000}]}`)
		case "/cover.jpg":
			var b bytes.Buffer
			_ = jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 64, 64)), nil)
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write(b.Bytes())
		case "/short":
			http.Redirect(w, r, "/landing", http.StatusFound)
		case "/landing":
			fmt.Fprint(w, `<a href="https://open.spotify.com/intl-de/track/`+songID+`?si=x">open</a>`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := spotifyBase
	spotifyBase = srv.URL
	t.Cleanup(func() { spotifyBase = old })
	return srv
}

func spotifyURL(t *testing.T, raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestSpotifyFacts(t *testing.T) {
	srv := fakeSpotify(t)
	r := newRegistry(t, true, nil)
	ctx := context.Background()

	for raw, want := range map[string]string{
		"https://open.spotify.com/track/" + songID + "?si=abc": "track " + songID,
		"https://open.spotify.com/intl-fa/album/" + albumID:    "album " + albumID,
		srv.URL + "/short":                          "track " + songID,
		"https://open.spotify.com/artist/" + songID: "artist " + songID,
	} {
		kind, id, err := r.spotifyTarget(ctx, spotifyURL(t, raw))
		if err != nil || kind+" "+id != want {
			t.Errorf("%s: %s %s %v", raw, kind, id, err)
		}
	}
	if _, _, err := r.spotifyTarget(ctx, spotifyURL(t, "https://open.spotify.com/")); err == nil {
		t.Error("the home page is not a song")
	}

	tr, err := r.spotifyTrackOf(ctx, songID)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Title != "Song Name (feat. Guest)" || strings.Join(tr.Artists, ",") != "Singer,Guest" || tr.Album != "Best & Last" ||
		tr.Year != "2021" || tr.Number != 3 || tr.Duration != 200 || tr.Cover != srv.URL+"/cover.jpg" {
		t.Errorf("facts: %+v", tr)
	}
	if songName(tr) != "Singer - Song Name (feat. Guest)" || songName(spotifyTrack{Title: "100% / Real?", Artists: []string{"-DJ"}}) != "DJ - 100 Real" {
		t.Errorf("names: %q", songName(tr))
	}
	if _, err := r.spotifyTrackOf(ctx, "0000000000000000000000"); err == nil {
		t.Error("a missing song is an error")
	}
}

func TestSpotifyMatching(t *testing.T) {
	song := spotifyTrack{Title: "Song Name (feat. Guest)", Artists: []string{"Singer", "Guest"}, Duration: 200}
	for _, c := range []struct {
		cand songCandidate
		ok   bool
	}{
		{songCandidate{title: "Singer - Song Name (Official Audio)", channel: "Singer", duration: 201}, true},
		{songCandidate{title: "Song Name", channel: "Singer - Topic", duration: 200}, true},
		{songCandidate{title: "Singer - Song Name", channel: "x", duration: 0}, true},
		{songCandidate{title: "Singer - Song Name (Official Video)", channel: "Singer", duration: 209}, false},
		{songCandidate{title: "Singer - Song Name (Slowed + Reverb)", channel: "Singer", duration: 200}, false},
		{songCandidate{title: "Singer - Song Name Live in Tehran", channel: "Singer", duration: 200}, false},
		{songCandidate{title: "Singer & Other - Song Name / Other Hit / Third One", channel: "m", duration: 200}, false},
		{songCandidate{title: "Singer - Another Track", channel: "Singer", duration: 200}, false},
	} {
		if got := c.cand.rate(song); got != c.ok {
			t.Errorf("%q: %v", c.cand.title, got)
		}
	}
	topic := songCandidate{title: "Song Name", channel: "Singer - Topic", duration: 200}
	lyrics := songCandidate{title: "Singer - Song Name (Lyrics)", channel: "7clouds", duration: 204}
	topic.rate(song)
	lyrics.rate(song)
	if topic.score <= lyrics.score {
		t.Errorf("the artist's own upload of the same length ranks first: %.2f vs %.2f", topic.score, lyrics.score)
	}

	persian := spotifyTrack{Title: "Khorshido Mah", Artists: []string{"Hamim"}, Duration: 180}
	dual := songCandidate{title: "Hamim - Khorshido Mah | همیم خورشیدو ماه", channel: "Hamim", duration: 181}
	if !dual.rate(persian) {
		t.Error("a title in two scripts still matches")
	}
	accents := songCandidate{title: "Beyonce - Halo", duration: 261}
	if !accents.rate(spotifyTrack{Title: "Halo", Artists: []string{"Beyoncé"}, Duration: 261}) {
		t.Error("accents do not matter")
	}
}

func TestSpotifyDownload(t *testing.T) {
	fakeSpotify(t)
	r := newRegistry(t, true, nil)
	fakeDownloader(t, r)
	ws, _ := r.Machine.Workspace("s")
	ffmpeg := exec.Command("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440", "-t", "2", "-c:a", "libmp3lame",
		filepath.Join(ws, "song_src.mp3")).Run() == nil
	d := r.downloaderInfo()
	d.FFmpeg = ffmpeg

	out := r.spotify(context.Background(), "s", spotifyURL(t, "https://open.spotify.com/track/"+songID), dlRequest{}, d)
	for _, want := range []string{`Downloaded "Singer - Song Name (feat. Guest).mp3"`, "«Song Name (feat. Guest)» · by Singer, Guest · 3:20",
		"album «Best & Last»; 2021", "matched on SoundCloud: «Singer - Song Name» by someone, 3 s from Spotify's length"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	files, _ := r.Machine.ListFiles("s")
	for _, f := range files {
		if strings.HasSuffix(f.Name, ".webp") || strings.HasSuffix(f.Name, ".jpg") || f.Name == spotifyTagged {
			t.Errorf("leftover %s", f.Name)
		}
	}
	if ffmpeg {
		if !strings.Contains(out, "Spotify's cover and tags added") {
			t.Errorf("tags: %s", out)
		}
		probe, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format_tags=title,artist,album,date,track:stream=codec_name",
			"-of", "default=nw=1", filepath.Join(ws, "Singer - Song Name (feat. Guest).mp3")).Output()
		for _, want := range []string{"codec_name=mp3", "codec_name=mjpeg", "TAG:title=Song Name (feat. Guest)", "TAG:artist=Singer, Guest",
			"TAG:album=Best & Last", "TAG:date=2021", "TAG:track=3"} {
			if err != nil || !strings.Contains(string(probe), want) {
				t.Errorf("tag %q: %s %v", want, probe, err)
			}
		}
	}

	out = r.spotify(context.Background(), "s", spotifyURL(t, "https://open.spotify.com/album/"+albumID), dlRequest{}, d)
	if !strings.Contains(out, "Spotify album «Best & Last» by Singer — 2 songs") || !strings.Contains(out, "2. Song Name (feat. Guest) — Singer, Guest · 3:20") ||
		!strings.Contains(out, "Nothing was downloaded yet") {
		t.Errorf("album list: %s", out)
	}
	out = r.spotify(context.Background(), "s", spotifyURL(t, "https://open.spotify.com/album/"+albumID), dlRequest{items: "1-2"}, d)
	if !strings.Contains(out, `Downloaded 1 file(s)`) || !strings.Contains(out, "song 0000000000000000000000") {
		t.Errorf("album items: %s", out)
	}
	if out = r.spotify(context.Background(), "s", spotifyURL(t, "https://open.spotify.com/album/"+albumID), dlRequest{items: "1-6"}, d); !strings.Contains(out, "at most 5") {
		t.Errorf("limit: %s", out)
	}
	if out = r.spotify(context.Background(), "s", spotifyURL(t, "https://open.spotify.com/episode/"+songID), dlRequest{}, d); !strings.Contains(out, "cannot be downloaded") {
		t.Errorf("episode: %s", out)
	}
	if _, err := net.LookupHost("open.spotify.com"); err == nil {
		if out = call(t, r, "s", ToolDownloadMedia, map[string]any{"url": "spotify:track:" + songID}); !strings.Contains(out, "matched on SoundCloud") {
			t.Errorf("a spotify: link goes the same way: %s", out)
		}
	}
}
