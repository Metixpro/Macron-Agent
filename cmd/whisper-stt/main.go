// Command whisper-stt serves speech to text for the bot on the server itself:
// an OpenAI-compatible /v1/audio/transcriptions in front of whisper.cpp.
// It listens on the loopback only, runs one recording at a time (a small
// server has a few cores), and turns any audio or video into the 16 kHz WAV
// whisper.cpp reads with ffmpeg first.
//
//	whisper-stt -model /opt/whisper/models/ggml-large-v3-turbo-q5_0.bin
//
// The flags have environment variables too (WHISPER_MODEL, WHISPER_CLI,
// WHISPER_LISTEN, WHISPER_THREADS, WHISPER_ARGS).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	maxUpload  = 26 << 20
	maxJobTime = 20 * time.Minute
)

type server struct {
	model, cli, ffmpeg, name string
	threads                  int
	extra                    []string
	slot                     chan struct{}
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func main() {
	threads, _ := strconv.Atoi(env("WHISPER_THREADS", "0"))
	listen := flag.String("listen", env("WHISPER_LISTEN", "127.0.0.1:8443"), "address (loopback only)")
	model := flag.String("model", env("WHISPER_MODEL", ""), "the ggml model file")
	cli := flag.String("cli", env("WHISPER_CLI", "whisper-cli"), "whisper.cpp's whisper-cli")
	name := flag.String("name", env("WHISPER_MODEL_NAME", "whisper-large-v3-turbo"), "the model's name in /v1/models")
	flag.IntVar(&threads, "threads", threads, "CPU threads (0 = all cores)")
	extra := flag.String("args", env("WHISPER_ARGS", ""), "more whisper-cli options, e.g. \"-bs 5\"")
	flag.Parse()

	if *model == "" {
		log.Fatal("whisper-stt: -model (or WHISPER_MODEL) is required")
	}
	if _, err := os.Stat(*model); err != nil {
		log.Fatalf("whisper-stt: the model: %v", err)
	}
	host, _, err := net.SplitHostPort(*listen)
	if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
		log.Fatalf("whisper-stt: %q is not a loopback address; speech to text stays on the server", *listen)
	}
	bin, err := exec.LookPath(*cli)
	if err != nil {
		log.Fatalf("whisper-stt: %s: %v", *cli, err)
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		log.Fatalf("whisper-stt: ffmpeg is needed: %v", err)
	}
	if threads <= 0 {
		threads = runtime.NumCPU()
	}
	s := &server{model: *model, cli: bin, ffmpeg: ffmpeg, name: *name, threads: threads, extra: strings.Fields(*extra),
		slot: make(chan struct{}, 1)}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/audio/transcriptions", s.transcribe)
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": []map[string]string{{"id": s.name, "object": "model"}}})
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	srv := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 20 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Printf("whisper-stt: %s on http://%s/v1 (%d threads)", filepath.Base(*model), *listen, threads)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"message": msg, "type": "invalid_request_error"}})
}

func (s *server) transcribe(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		fail(w, http.StatusBadRequest, "a multipart form with the audio as 'file' is expected: "+err.Error())
		return
	}
	defer r.MultipartForm.RemoveAll()
	f, _, err := r.FormFile("file")
	if err != nil {
		fail(w, http.StatusBadRequest, "the form has no 'file'")
		return
	}
	defer f.Close()
	lang := strings.ToLower(strings.TrimSpace(r.FormValue("language")))
	if lang == "" {
		lang = "auto"
	} else if len(lang) != 2 {
		fail(w, http.StatusBadRequest, "language must be an ISO-639-1 code such as fa or en")
		return
	}

	dir, err := os.MkdirTemp("", "whisper-stt-")
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer os.RemoveAll(dir)
	in := filepath.Join(dir, "in")
	out, err := os.Create(in)
	if err == nil {
		_, err = io.Copy(out, f)
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}

	// One recording at a time; the others wait while their callers do.
	select {
	case s.slot <- struct{}{}:
		defer func() { <-s.slot }()
	case <-r.Context().Done():
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), maxJobTime)
	defer cancel()
	start := time.Now()
	text, err := s.run(ctx, dir, in, lang, strings.TrimSpace(r.FormValue("prompt")))
	if err != nil {
		if ctx.Err() != nil {
			fail(w, http.StatusGatewayTimeout, "the transcription took too long")
			return
		}
		fail(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	log.Printf("whisper-stt: %d characters in %s", len([]rune(text)), time.Since(start).Round(100*time.Millisecond))
	if r.FormValue("response_format") == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, text)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text})
}

// run converts the upload to the WAV whisper.cpp reads and writes it down.
func (s *server) run(ctx context.Context, dir, in, lang, prompt string) (string, error) {
	wav := filepath.Join(dir, "audio.wav")
	conv := exec.CommandContext(ctx, s.ffmpeg, "-nostdin", "-y", "-v", "error", "-i", in, "-vn", "-ac", "1", "-ar", "16000",
		"-c:a", "pcm_s16le", wav)
	if msg, err := conv.CombinedOutput(); err != nil {
		return "", fmt.Errorf("the audio could not be read: %s", lastLine(string(msg), err))
	}
	base := filepath.Join(dir, "out")
	args := []string{"-m", s.model, "-f", wav, "-l", lang, "-t", strconv.Itoa(s.threads), "-nt", "-np", "-otxt", "-of", base}
	if prompt != "" {
		args = append(args, "--prompt", prompt)
	}
	args = append(args, s.extra...)
	cmd := exec.CommandContext(ctx, s.cli, args...)
	if msg, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("whisper failed: %s", lastLine(string(msg), err))
	}
	data, err := os.ReadFile(base + ".txt")
	if err != nil {
		return "", err
	}
	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, " "), nil
}

func lastLine(out string, err error) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if l := strings.TrimSpace(lines[len(lines)-1]); l != "" {
		return l
	}
	return err.Error()
}
