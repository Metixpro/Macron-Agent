package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const ToolSong = "generate_song"

var ErrMusicKey = errors.New("the song service did not accept its key")

const (
	songCallback   = "https://example.com/waze-song-callback"
	songMaxAudio   = 40 << 20
	songMaxCover   = 4 << 20
	songMinLines   = 4
	songMaxLyrics  = 3000
	songBudget     = 7 * time.Minute
	maxSongsAtOnce = 3
)

var (
	songPollEvery = 6 * time.Second
	songSlots     = make(chan struct{}, maxSongsAtOnce)
)

func (r *Registry) SetMusicKey(key func() string) { r.musicKey = key }

func (r *Registry) MusicKey() string { return r.musicKeyNow() }

func (r *Registry) musicKeyNow() string {
	if r.musicKey == nil {
		return ""
	}
	return strings.TrimSpace(r.musicKey())
}

func (r *Registry) SetMusicAPI(url string) {
	r.musicAPI = url
	if url != "" {
		r.registerSongTool()
	}
}

func (r *Registry) SongReady() bool { return r.musicAPI != "" && r.musicKeyNow() != "" }

func (r *Registry) registerSongTool() {
	r.add(ToolSong,
		"Make a full song with a singer from lyrics you write, or with instrumental=true a real-sounding track with no voice: "+
			"two versions of 2–3 minutes; it takes about two minutes.",
		map[string]any{
			"lyrics":       prop("string", "[verse], [chorus], [bridge] sections of 2–6 short singable lines, in the language to sing (not for instrumental)"),
			"title":        prop("string", ""),
			"style":        prop("string", "English tags: genre, mood, instruments, e.g. 'Persian pop ballad, piano, sad' (default Pop)"),
			"gender":       enumProp("The voice", "male", "female"),
			"instrumental": prop("boolean", "True: music only, no singing (a beat, background music, a soundtrack)"),
			"filename":     prop("string", ""),
		}, nil, oneDownload(r.generateSong))
}

func (r *Registry) CheckMusicKey(ctx context.Context, key string) error {
	if r.musicAPI == "" {
		return errors.New("the song service is turned off (WAZE_MUSIC_API_URL=off)")
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, r.musicAPI, strings.NewReader("{}"))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-API-Key", key)
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("the song service is unreachable: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrMusicKey
	case http.StatusBadRequest, http.StatusOK:
		return nil
	}
	return fmt.Errorf("the song service answered HTTP %d", resp.StatusCode)
}

type songVersion struct {
	audio, cover string
	seconds      float64
}

func (r *Registry) generateSong(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	instrumental := boolArg(args, "instrumental")
	lyrics := strings.TrimSpace(rawStrArg(args, "lyrics"))
	if !instrumental {
		if lyrics == "" {
			return "error: lyrics is required: write the song's words in sections like [verse] and [chorus] " +
				"(or pass instrumental=true for music with no singing)", nil
		}
		if n := len([]rune(lyrics)); n > songMaxLyrics {
			return fmt.Sprintf("error: the lyrics are too long (%d characters, at most %d): a song needs about 8 to 24 lines", n, songMaxLyrics), nil
		}
		if lines := lyricLines(lyrics); lines < songMinLines {
			return fmt.Sprintf("error: the lyrics have only %d lines: write a real song, with at least a [verse] and a [chorus] of several short singable lines each", lines), nil
		}
	}
	if !r.SongReady() {
		return "the song service is not set up on this server (no API key). Tell the user AI songs are not available yet; " +
			"an electronic instrumental can still be made with " + ToolMusic + ".", nil
	}
	style := cleanStyle(strArg(args, "style"))
	gender := songGender(strArg(args, "gender"))
	if instrumental {

		lyrics, style, gender = instrumentalLyrics, instrumentalStyle(strArg(args, "style")), ""
	}
	title := cleanTitle(strArg(args, "title"))
	if title == "" && instrumental {
		title = styleTitle(style)
	}
	if title == "" {
		title = cleanTitle(firstLyric(lyrics))
	}
	if title == "" {
		title = "Song"
	}

	base := "Macron_AI_-_" + sanitizeBase(title)
	if fn := strArg(args, "filename"); fn != "" {
		base = strings.TrimSuffix(sanitizeBase(fn), ".mp3")
	}
	if base == "" || base == "Macron_AI_-_" {
		base = fmt.Sprintf("Macron_AI_-_song_%d", time.Now().Unix())
	}
	files, seconds, err := r.makeSong(ctx, sessionID, lyrics, title, style, gender, base, 2, true)
	if err != nil {
		return songFailure(err), nil
	}
	lyricsFile := ""
	if !instrumental {
		lyricsFile = r.saveLyrics(sessionID, base, title, style, gender, lyrics)
	}
	var sb strings.Builder
	sb.WriteString("Song created and queued for delivery.")
	for i, f := range files {
		label := "File"
		if i > 0 {
			label = fmt.Sprintf("File (version %d)", i+1)
		}
		fmt.Fprintf(&sb, "\n- %s: %s (%s)", label, f.name, humanSize(f.size))
	}
	fmt.Fprintf(&sb, "\n- Title: %s", title)
	fmt.Fprintf(&sb, "\n- Style: %s", style)
	switch {
	case instrumental:
		sb.WriteString("\n- Voice: none (instrumental)")
	case gender != "":
		fmt.Fprintf(&sb, "\n- Voice: %s", gender)
	}
	if seconds > 0 {
		fmt.Fprintf(&sb, "\n- Length: %d:%02d", int(seconds)/60, int(seconds)%60)
	}
	sb.WriteString("\n- Engine: AI song service")
	if len(files) > 1 {
		sb.WriteString("\n- Two versions were made (the service makes two each time); both go to the user. Say so in one short line.")
	}
	if lyricsFile != "" {
		fmt.Fprintf(&sb, "\n- Lyrics file: %s (saved beside the song, also in the mp3's tags; it is not sent by itself)", lyricsFile)
		sb.WriteString("\n- The user has not seen the lyrics unless you already wrote them in this conversation: put them in your answer, " +
			"one line each, so they stay in the conversation. When they are asked for later, take them from that answer or read the lyrics " +
			"file with read_file; never transcribe a song you made with a speech tool.")
	}
	if instrumental {
		sb.WriteString("\n- It was asked for with no singing; you cannot hear it, so do not promise there is no voice at all. " +
			"If the user says a voice is in it, say sorry plainly and offer " + ToolMusic + " (a synthesizer, always voice-free) or another try.")
	}
	return sb.String(), nil
}

func (r *Registry) saveLyrics(sessionID, base, title, style, gender, lyrics string) string {
	voice := gender
	if voice == "" {
		voice = "default"
	}
	text := fmt.Sprintf("%s\n\n%s\n\n—\nStyle: %s · Voice: %s · Macron AI\n", title, strings.TrimSpace(lyrics), style, voice)
	name := base + ".txt"
	if err := r.Machine.WriteFile(sessionID, name, []byte(text)); err != nil {
		log.Printf("generate_song: the lyrics could not be saved: %v", err)
		return ""
	}
	return name
}

func (r *Registry) makeSong(ctx context.Context, sessionID, lyrics, title, style, gender, base string, keep int, tags bool) ([]savedImage, float64, error) {
	select {
	case songSlots <- struct{}{}:
		defer func() { <-songSlots }()
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, songBudget)
	defer cancel()

	ReportStatus(ctx, "song · queue")
	statusURL, err := r.createSong(ctx, sessionID, lyrics, title, style, gender)
	if err != nil {
		return nil, 0, err
	}
	versions, err := r.waitSong(ctx, statusURL)
	if err != nil {
		return nil, 0, err
	}

	ReportStatus(ctx, "song · saving")
	var files []savedImage
	var seconds float64
	for i, v := range versions {
		if len(files) >= keep {
			break
		}
		name := base + ".mp3"
		if i > 0 {
			name = fmt.Sprintf("%s_%d.mp3", base, i+1)
		}
		size, err := r.saveSong(ctx, sessionID, v, name, title, lyrics, tags)
		if err != nil {
			log.Printf("generate_song: version %d could not be saved: %v", i+1, err)
			continue
		}
		files = append(files, savedImage{name: name, size: size})
		seconds = max(seconds, v.seconds)
	}
	if len(files) == 0 {
		return nil, 0, errors.New("the songs could not be downloaded")
	}
	return files, seconds, nil
}

func songFailure(err error) string {
	switch {
	case errors.Is(err, ErrMusicKey):
		return "song making failed: the song service did not accept its key on this server. Tell the user it is temporarily unavailable; " +
			"an instrumental can still be made with " + ToolMusic + "."
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "song making was stopped or took too long. Tell the user it did not finish; they can ask again."
	}
	return fmt.Sprintf("song making failed: %v. Tell the user it is temporarily unavailable; do NOT offer a different song as if it were this one.", err)
}

func (r *Registry) createSong(ctx context.Context, sessionID, lyrics, title, style, gender string) (string, error) {
	body := map[string]any{"user_id": editUserID(sessionID), "callback_url": songCallback, "lyrics": lyrics, "title": title, "style": style}
	if gender != "" {
		body["gender"] = gender
	}
	payload, _ := json.Marshal(body)
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, r.musicAPI, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-API-Key", r.musicKeyNow())
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("the song service is unreachable: %w", err)
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", ErrMusicKey
	}
	var out struct {
		Status         any    `json:"status"`
		TaskID         string `json:"task_id"`
		CheckStatusURL string `json:"check_status_url"`
		Error          string `json:"error"`
		Message        string `json:"message"`
	}
	if json.Unmarshal(data, &out) != nil {
		return "", fmt.Errorf("the song service answered HTTP %d with an unexpected body", resp.StatusCode)
	}
	if ok, _ := out.Status.(bool); !ok {
		msg := strings.TrimSpace(out.Error)
		if msg == "" {
			msg = strings.TrimSpace(out.Message)
		}
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return "", errors.New(textutil.TruncateRunes(msg, 200, "…"))
	}
	if out.TaskID == "" {
		return "", errors.New("the song service gave no task id")
	}
	if out.CheckStatusURL != "" && sameHost(out.CheckStatusURL, r.musicAPI) {
		return out.CheckStatusURL, nil
	}
	u, err := url.Parse(r.musicAPI)
	if err != nil {
		return "", err
	}
	u.Path = path.Dir(u.Path) + "/"
	u.RawQuery = url.Values{"task_id": {out.TaskID}}.Encode()
	return u.String(), nil
}

func (r *Registry) waitSong(ctx context.Context, statusURL string) ([]songVersion, error) {
	start := time.Now()
	missing := 0
	for {
		state, versions, problem, err := r.songStatus(ctx, statusURL)
		switch {
		case err != nil && ctx.Err() != nil:
			return nil, fmt.Errorf("the song was not ready in time (%s): %w", time.Since(start).Round(time.Second), context.DeadlineExceeded)
		case errors.Is(err, ErrMusicKey):
			return nil, err
		case err != nil:
			if missing++; missing > 3 {
				return nil, err
			}
		case problem != "":
			return nil, errors.New(problem)
		case len(versions) > 0:
			return versions, nil
		default:
			missing = 0
			ReportStatus(ctx, fmt.Sprintf("song · %s · %ds", state, int(time.Since(start).Seconds())))
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("the song was not ready in time (%s): %w", time.Since(start).Round(time.Second), context.DeadlineExceeded)
		case <-time.After(songPollEvery):
		}
	}
}

func (r *Registry) songStatus(ctx context.Context, statusURL string) (state string, versions []songVersion, problem string, err error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, statusURL, nil)
	if err != nil {
		return "", nil, "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-API-Key", r.musicKeyNow())
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return "", nil, "", err
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", nil, "", ErrMusicKey
	}
	var out struct {
		Status bool `json:"status"`
		Error  string
		Task   struct {
			Status       string `json:"status"`
			ErrorMessage string `json:"error_message"`
			Songs        []struct {
				Status   string  `json:"status"`
				AudioURL string  `json:"audio_url"`
				CoverURL string  `json:"cover_url"`
				ImageURL string  `json:"image_url"`
				Duration float64 `json:"duration"`
			} `json:"songs"`
		} `json:"task"`
	}
	if json.Unmarshal(data, &out) != nil {
		return "", nil, "", fmt.Errorf("the status page answered HTTP %d with an unexpected body", resp.StatusCode)
	}
	state = strings.ToLower(strings.TrimSpace(out.Task.Status))
	switch state {
	case "success", "succeeded", "completed", "complete", "done", "finished":
		seen := map[string]bool{}
		for _, s := range out.Task.Songs {
			if !strings.HasPrefix(s.AudioURL, "http") || seen[s.AudioURL] {
				continue
			}
			if st := strings.ToLower(s.Status); st != "" && st != "complete" && st != "completed" && st != "success" {
				continue
			}
			seen[s.AudioURL] = true
			cover := s.CoverURL
			if cover == "" {
				cover = s.ImageURL
			}
			versions = append(versions, songVersion{audio: s.AudioURL, cover: cover, seconds: s.Duration})
			if len(versions) == 2 {
				break
			}
		}
		if len(versions) == 0 {
			return state, nil, "the service finished without a song", nil
		}
		return state, versions, "", nil
	case "failed", "failure", "error", "canceled", "cancelled", "rejected", "expired":
		msg := strings.TrimSpace(out.Task.ErrorMessage)
		if msg == "" {
			msg = "the service could not make the song"
		}
		return state, nil, textutil.TruncateRunes(msg, 200, "…"), nil
	case "":
		return "", nil, "", errors.New("the status page said nothing about the task")
	}
	return state, nil, "", nil
}

func (r *Registry) saveSong(ctx context.Context, sessionID string, v songVersion, name, title, lyrics string, withTags bool) (int64, error) {
	dctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(dctx, http.MethodGet, v.audio, nil)
	if err != nil {
		return 0, errors.New("the song service gave an invalid link")
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("downloading the song: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("downloading the song: HTTP %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "audio/") && !strings.HasPrefix(ct, "application/octet-stream") {
		return 0, fmt.Errorf("downloading the song: unexpected content type %q", ct)
	}
	if _, err := r.saveStream(sessionID, name, resp.Body, songMaxAudio); err != nil {
		return 0, err
	}
	if withTags {

		r.embedSongTags(ctx, sessionID, name, title, lyrics, r.fetchCover(ctx, v.cover))
	}
	if _, fi, err := r.Machine.Open(sessionID, name); err == nil {
		return fi.Size(), nil
	}
	return 0, errors.New("the song was not saved")
}

func (r *Registry) fetchCover(ctx context.Context, link string) []byte {
	if !strings.HasPrefix(link, "https://") && !sameHost(link, r.musicAPI) {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, link, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, songMaxCover+1))
	if err != nil || len(data) > songMaxCover {
		return nil
	}

	if _, format, err := image.DecodeConfig(bytes.NewReader(data)); err != nil || (format != "jpeg" && format != "png") {
		return nil
	}
	return data
}

func (r *Registry) embedSongTags(ctx context.Context, sessionID, name, title, lyrics string, cover []byte) {
	args := []string{"ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-i", "./" + name}
	if len(cover) > 0 {
		cpath := scratchDir + "/cover_" + strings.TrimSuffix(name, ".mp3") + ".jpg"
		if r.Machine.WriteFile(sessionID, cpath, cover) == nil {
			defer r.Machine.RemoveAll(sessionID, cpath)
			args = append(args, "-i", "./"+cpath, "-map", "0:a", "-map", "1:v", "-c", "copy", "-metadata:s:v", "title=Album cover", "-metadata:s:v", "comment=Cover (front)")
		} else {
			cover = nil
		}
	}
	if len(cover) == 0 {
		args = append(args, "-map", "0:a", "-c", "copy")
	}
	args = append(args, "-id3v2_version", "3", "-metadata", "title="+title, "-metadata", "artist=Macron AI")
	if lyrics != "" && lyrics != instrumentalLyrics {
		args = append(args, "-metadata", "lyrics="+lyrics)
	}
	tmp := scratchDir + "/tagged_" + name
	defer r.Machine.RemoveAll(sessionID, tmp)
	res := r.Machine.Run(ctx, sessionID, machine.RunOptions{Args: append(args, "./"+tmp), Timeout: 60 * time.Second})
	if res.ExitCode != 0 {
		return
	}
	tagged, _, err := r.Machine.ReadFile(sessionID, tmp, songMaxAudio+songMaxCover)
	if err != nil || len(tagged) < 1024 {
		return
	}
	_ = r.Machine.WriteFile(sessionID, name, tagged)
}

func lyricLines(s string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || (strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]")) {
			continue
		}
		n++
	}
	return n
}

func firstLyric(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !(strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]")) {
			return line
		}
	}
	return ""
}

func cleanTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.NewReplacer("\"", "", "`", "", "*", "", "[", "", "]", "", "/", " ", "\\", " ").Replace(s)
	return strings.TrimSpace(textutil.TruncateRunes(s, 48, ""))
}

func cleanStyle(s string) string {
	if s = cleanStyleText(s); s == "" {
		return "Pop"
	}
	return s
}

func cleanStyleText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, s)
	return textutil.TruncateRunes(s, 200, "")
}

const instrumentalLyrics = "[Intro]\n[Instrumental Break]\n[Instrumental Solo]\n[Instrumental Break]\n[Outro]"

func instrumentalStyle(style string) string {
	const tags = "instrumental, no vocals, no singing"
	style = cleanStyleText(style)
	if style == "" {
		return tags
	}
	return textutil.TruncateRunes(tags+", "+style, 200, "")
}

func styleTitle(style string) string {
	for _, tag := range strings.Split(style, ",") {
		tag = strings.TrimSpace(tag)
		switch strings.ToLower(tag) {
		case "", "instrumental", "no vocals", "no singing":
			continue
		}
		words := strings.Fields(tag)
		if len(words) > 3 {
			words = words[:3]
		}
		for i, w := range words {
			r := []rune(w)
			r[0] = unicode.ToUpper(r[0])
			words[i] = string(r)
		}
		return cleanTitle(strings.Join(words, " "))
	}
	return "Instrumental"
}

func songGender(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "male", "man", "m", "مرد", "مردانه":
		return "male"
	case "female", "woman", "f", "زن", "زنانه":
		return "female"
	}
	return ""
}
