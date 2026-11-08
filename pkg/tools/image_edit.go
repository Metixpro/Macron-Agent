package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const ToolEditImage = "edit_image"

const (
	maxEditSources = 4
	maxEditUpload  = 20 << 20

	editPlaceholderCallback = "https://example.com/waze-image-callback"
)

var (
	editPollEvery = 4 * time.Second

	uploadHosts = []uploadHost{
		{name: "uguu", url: "https://uguu.se/upload", field: "files[]", parse: parseUguu},
		{name: "litterbox", url: "https://litterbox.catbox.moe/resources/internals/api.php", field: "fileToUpload",
			extra: map[string]string{"reqtype": "fileupload", "time": "1h"}, parse: parsePlainURL},
		{name: "tmpfiles", url: "https://tmpfiles.org/api/v1/upload", field: "file", parse: parseTmpfiles},
	}
)

func (r *Registry) SetImageEditKey(key func() string) { r.imageEditKey = key }

func (r *Registry) ImageEditKey() string { return r.editKey() }

func (r *Registry) editKey() string {
	if r.imageEditKey == nil {
		return ""
	}
	return strings.TrimSpace(r.imageEditKey())
}

func (r *Registry) Usable(name, sessionID string) bool {
	switch name {
	case ToolSaveSkill:
		return r.SkillWritable(sessionID)
	case ToolEditImage:
		return r.editKey() != ""
	case ToolSong:
		return r.SongReady()
	}
	if r.Builtin(name) {
		return true
	}
	p := r.pluginTools()
	return p != nil && p.Has(name) && p.Allowed(name, sessionID)
}

func (r *Registry) registerImageEditTool() {
	r.add(ToolEditImage,
		"Edit pictures with AI: background, style, lighting, add or remove things or people, retouch, colorize, combine several.",
		map[string]any{
			"prompt":       prop("string", "In English, saying what to keep, e.g. 'snowy forest background, keep the person exactly the same'"),
			"images":       arrayProp("string", "1-4 workspace files or image links; several are combined"),
			"aspect_ratio": enumProp("", "1:1", "16:9", "9:16", "4:3", "3:4"),
			"filename":     prop("string", ""),
		}, []string{"prompt", "images"}, oneImage(r.editImage))
}

func (r *Registry) editImage(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	if r.editKey() == "" {
		return "image editing is not set up on this server (no API key). Do simple edits (crop, resize, rotate, brightness, contrast, blur, " +
			"text, borders) with code (Pillow/OpenCV) instead; for AI edits tell the user it is not available yet.", nil
	}
	prompt := strArg(args, "prompt")
	switch {
	case prompt == "":
		return "error: prompt is required", nil
	case len([]rune(prompt)) > 2000:
		return "error: prompt is too long (max 2000 characters)", nil
	}
	var sources []string
	if list, ok := args["images"].([]any); ok {
		for _, v := range list {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				sources = append(sources, strings.TrimSpace(s))
			}
		}
	} else if s := strArg(args, "images"); s != "" {
		sources = append(sources, s)
	}
	if s := strArg(args, "image"); s != "" {
		sources = append(sources, s)
	}
	switch {
	case len(sources) == 0:
		return "error: images is required: the workspace file name(s) or link(s) of the picture(s) to edit", nil
	case len(sources) > maxEditSources:
		return fmt.Sprintf("error: at most %d pictures can be edited together", maxEditSources), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()

	links := make([]string, 0, len(sources))
	for _, src := range sources {
		if u, err := url.Parse(src); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
			links = append(links, src)
			continue
		}
		ReportStatus(ctx, "uploading")
		link, err := r.publishImage(ctx, sessionID, src)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		links = append(links, link)
	}
	base := strings.TrimSuffix(path.Base(sources[0]), path.Ext(sources[0]))
	name := outputName(strArg(args, "filename"), "edited_"+slug(base+" "+prompt, 24), imageExts, ".png")
	n, err := r.nanoTaskImage(ctx, sessionID, prompt, links, strArg(args, "aspect_ratio"), name)
	if err != nil {
		return fmt.Sprintf("image editing failed: %v. Tell the user it is temporarily unavailable (a simple edit can still be done with code). "+
			"Never send the original picture as if it were edited.", err), nil
	}
	return fmt.Sprintf("Image edited and queued for delivery.\n- File: %s (%s)\n- Edit: %s\n- Engine: Nano Banana", name, humanSize(n), prompt), nil
}

func editUserID(sessionID string) string {
	h := sha256.Sum256([]byte("waze:" + sessionID))
	return "waze-" + hex.EncodeToString(h[:6])
}

func (r *Registry) nanoTaskImage(ctx context.Context, sessionID, prompt string, sources []string, aspect, name string) (int64, error) {
	if r.imageEditAPI == "" || r.editKey() == "" {
		return 0, errors.New("the Nano Banana task service is not set up")
	}
	statusURL, err := r.createNanoTask(ctx, sessionID, prompt, sources, aspect)
	if err != nil {
		return 0, err
	}
	link, err := r.waitNanoTask(ctx, statusURL)
	if err != nil {
		return 0, err
	}
	dctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(dctx, http.MethodGet, link, nil)
	if err != nil {
		return 0, errors.New("the service gave an invalid image link")
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("downloading the picture: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("downloading the picture: HTTP %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/") && !strings.HasPrefix(ct, "application/octet-stream") {
		return 0, fmt.Errorf("downloading the picture: unexpected content type %q", ct)
	}
	return r.saveStream(sessionID, name, resp.Body, 20<<20)
}

func (r *Registry) createNanoTask(ctx context.Context, sessionID, prompt string, sources []string, aspect string) (string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("user_id", editUserID(sessionID))
	_ = w.WriteField("callback_url", editPlaceholderCallback)
	_ = w.WriteField("prompt", prompt)
	for _, s := range sources {
		_ = w.WriteField("image_urls[]", s)
	}
	if aspect != "" {
		_ = w.WriteField("aspect", aspect)
	}
	_ = w.Close()
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, r.imageEditAPI, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-API-Key", r.editKey())
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("the service is unreachable: %w", err)
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	var out map[string]any
	if json.Unmarshal(data, &out) != nil {
		return "", fmt.Errorf("the service answered HTTP %d with an unexpected body", resp.StatusCode)
	}
	if ok, _ := out["status"].(bool); !ok {
		return "", errors.New(serviceError(out, resp.StatusCode))
	}
	id, _ := out["task_id"].(string)
	if id == "" {
		return "", errors.New("the service gave no task id")
	}
	if s, _ := out["check_status_url"].(string); s != "" && sameHost(s, r.imageEditAPI) {
		return s, nil
	}
	u, err := url.Parse(r.imageEditAPI)
	if err != nil {
		return "", err
	}
	u.Path = path.Join(path.Dir(u.Path), "task.php")
	u.RawQuery = url.Values{"task_id": {id}}.Encode()
	return u.String(), nil
}

func sameHost(a, b string) bool {
	ua, err1 := url.Parse(a)
	ub, err2 := url.Parse(b)
	return err1 == nil && err2 == nil && strings.EqualFold(ua.Host, ub.Host) && (ua.Scheme == "https" || ua.Scheme == ub.Scheme)
}

func (r *Registry) waitNanoTask(ctx context.Context, statusURL string) (string, error) {
	start := time.Now()
	missing := 0
	for {
		state, link, problem, err := r.nanoTaskStatus(ctx, statusURL)
		switch {
		case err != nil && ctx.Err() != nil:
			return "", fmt.Errorf("the picture was not ready in time (%s)", time.Since(start).Round(time.Second))
		case err != nil:
			missing++
			if missing > 3 {
				return "", err
			}
		case link != "":
			return link, nil
		case problem != "":
			return "", errors.New(problem)
		default:
			missing = 0
			ReportStatus(ctx, fmt.Sprintf("%s · %ds", state, int(time.Since(start).Seconds())))
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("the picture was not ready in time (%s)", time.Since(start).Round(time.Second))
		case <-time.After(editPollEvery):
		}
	}
}

func (r *Registry) nanoTaskStatus(ctx context.Context, statusURL string) (state, link, problem string, err error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, statusURL, nil)
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-API-Key", r.editKey())
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return "", "", "", err
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	var out map[string]any
	if json.Unmarshal(data, &out) != nil {
		return "", "", "", fmt.Errorf("the status page answered HTTP %d with an unexpected body", resp.StatusCode)
	}
	state, _ = out["status"].(string)
	state = strings.ToLower(strings.TrimSpace(state))
	switch state {
	case "success", "succeeded", "completed", "complete", "done", "finished":
		for _, k := range []string{"processed_image_url", "image_url", "remote_image_url"} {
			if s, _ := out[k].(string); strings.HasPrefix(s, "http") {
				return state, s, "", nil
			}
		}
		if list, _ := out["image_urls"].([]any); len(list) > 0 {
			if s, _ := list[0].(string); strings.HasPrefix(s, "http") {
				return state, s, "", nil
			}
		}
		return state, "", "the service finished without a picture", nil
	case "failed", "failure", "error", "canceled", "cancelled", "rejected", "expired":
		if resp.StatusCode == http.StatusNotFound {
			return state, "", "", errors.New(serviceError(out, resp.StatusCode))
		}
		return state, "", serviceError(out, resp.StatusCode), nil
	case "":
		return "", "", "", errors.New(serviceError(out, resp.StatusCode))
	}
	return state, "", "", nil
}

func serviceError(out map[string]any, status int) string {
	for _, k := range []string{"error_message", "error", "message"} {
		if s, _ := out[k].(string); strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	if logs, _ := out["error_log"].([]any); len(logs) > 0 {
		if last, _ := logs[len(logs)-1].(map[string]any); last != nil {
			if s, _ := last["error"].(string); s != "" {
				return s
			}
		}
	}
	return fmt.Sprintf("HTTP %d", status)
}

type uploadHost struct {
	name, url, field string
	extra            map[string]string
	parse            func([]byte) (string, error)
}

func (r *Registry) publishImage(ctx context.Context, sessionID, name string) (string, error) {
	f, fi, err := r.Machine.Open(sessionID, name)
	if err != nil {
		return "", fmt.Errorf("picture %q not found in the workspace", name)
	}
	defer f.Close()
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%q is not a file", name)
	}
	if fi.Size() > maxEditUpload {
		return "", fmt.Errorf("%q is larger than %s", name, humanSize(maxEditUpload))
	}
	data, err := io.ReadAll(io.LimitReader(f, maxEditUpload+1))
	if err != nil {
		return "", err
	}
	ct := http.DetectContentType(data)
	if !strings.HasPrefix(ct, "image/") {
		return "", fmt.Errorf("%q is not a picture (%s)", name, ct)
	}
	var problems []string
	for _, h := range uploadHosts {
		link, err := r.uploadTo(ctx, h, path.Base(name), data)
		if err == nil {
			return link, nil
		}
		problems = append(problems, h.name+": "+err.Error())
	}
	return "", fmt.Errorf("the picture could not be prepared for the editor (%s)", strings.Join(problems, "; "))
}

func (r *Registry) uploadTo(ctx context.Context, h uploadHost, filename string, data []byte) (string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for k, v := range h.extra {
		_ = w.WriteField(k, v)
	}
	part, err := w.CreateFormFile(h.field, sanitizeBase(filename))
	if err != nil {
		return "", err
	}
	_, _ = part.Write(data)
	_ = w.Close()
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, h.url, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	link, err := h.parse(out)
	if err != nil {
		return "", err
	}
	if u, err := url.Parse(link); err != nil || u.Scheme != "https" || u.Host == "" {
		return "", errors.New("no link in the answer")
	}
	return link, nil
}

func parseUguu(b []byte) (string, error) {
	var out struct {
		Success bool `json:"success"`
		Files   []struct {
			URL string `json:"url"`
		} `json:"files"`
	}
	if err := json.Unmarshal(b, &out); err != nil || !out.Success || len(out.Files) == 0 {
		return "", errors.New("unexpected answer")
	}
	return out.Files[0].URL, nil
}

func parsePlainURL(b []byte) (string, error) {
	s := strings.TrimSpace(string(b))
	if !strings.HasPrefix(s, "https://") || strings.ContainsAny(s, " \n<") {
		return "", errors.New("unexpected answer")
	}
	return s, nil
}

func parseTmpfiles(b []byte) (string, error) {
	var out struct {
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.Data.URL == "" {
		return "", errors.New("unexpected answer")
	}
	link := strings.Replace(out.Data.URL, "http://", "https://", 1)
	return strings.Replace(link, "tmpfiles.org/", "tmpfiles.org/dl/", 1), nil
}

var ErrImageEditKey = errors.New("the image editing service refused the key")

func (r *Registry) CheckImageEditKey(ctx context.Context, key string) error {
	if r.imageEditAPI == "" {
		return errors.New("image editing is turned off (WAZE_IMAGE_EDIT_URL=off)")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.imageEditAPI, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", key)
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("the service is unreachable: %w", err)
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ErrImageEditKey
	}
	var out map[string]any
	if json.Unmarshal(data, &out) != nil {
		return fmt.Errorf("the service answered HTTP %d with an unexpected body", resp.StatusCode)
	}
	if strings.Contains(strings.ToLower(serviceError(out, resp.StatusCode)), "api key") {
		return ErrImageEditKey
	}
	return nil
}
