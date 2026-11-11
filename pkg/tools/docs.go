package tools

import (
	"archive/zip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Metixpro/macron-agent/pkg/search"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const (
	ToolDocumentReader = "document_reader"

	docIndexDir     = ".docindex"
	docMaxBytes     = 200 << 20
	docMaxPages     = 3000
	docPartChars    = 3000
	docTextMaxBytes = 50 << 20
	chunkTarget     = 900
	chunkMax        = 1400
	searchDefaultK  = 6
	searchMaxK      = 12
	readMaxPages    = 10
	readMaxChars    = 14000
	docCacheSize    = 6
	docExtractLimit = 5 * time.Minute
)

var docExts = map[string]bool{".pdf": true, ".docx": true, ".epub": true, ".txt": true, ".md": true, ".markdown": true,
	".html": true, ".htm": true, ".xhtml": true, ".csv": true, ".tsv": true, ".json": true, ".log": true, ".srt": true, ".vtt": true, ".xml": true,
	".xlsx": true, ".xlsm": true, ".pptx": true, ".db": true, ".sqlite": true, ".sqlite3": true}

var sqliteExts = map[string]bool{".db": true, ".sqlite": true, ".sqlite3": true}

type docOutlineItem struct {
	Title string `json:"title"`
	Page  int    `json:"page"`
	Level int    `json:"level"`
}

type docFile struct {
	Name       string           `json:"name"`
	Size       int64            `json:"size"`
	ModTime    int64            `json:"mtime"`
	Title      string           `json:"title"`
	Paged      bool             `json:"paged"`
	TotalPages int              `json:"total_pages"`
	Pages      []string         `json:"pages"`
	Outline    []docOutlineItem `json:"outline"`
}

func (d *docFile) unit() string {
	if d.Paged {
		return "page"
	}
	return "part"
}

type docChunk struct {
	page   int
	text   string
	folded string
	terms  map[string]int
	length int
}

type docIndex struct {
	file   *docFile
	chunks []docChunk
	df     map[string]int
	avgLen float64
}

type docCacheEntry struct {
	key          string
	size, mtime  int64
	idx          *docIndex
	lastAccessed time.Time
}

type docCache struct {
	mu       sync.Mutex
	entries  []*docCacheEntry
	building map[string]*sync.Mutex
}

func newDocCache() *docCache { return &docCache{building: map[string]*sync.Mutex{}} }

func (c *docCache) get(key string, size, mtime int64) *docIndex {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.entries {
		if e.key == key && e.size == size && e.mtime == mtime {
			e.lastAccessed = time.Now()
			return e.idx
		}
	}
	return nil
}

func (c *docCache) put(key string, size, mtime int64, idx *docIndex) {
	c.mu.Lock()
	defer c.mu.Unlock()
	kept := c.entries[:0]
	for _, e := range c.entries {
		if e.key != key {
			kept = append(kept, e)
		}
	}
	c.entries = append(kept, &docCacheEntry{key: key, size: size, mtime: mtime, idx: idx, lastAccessed: time.Now()})
	if len(c.entries) > docCacheSize {
		sort.Slice(c.entries, func(i, j int) bool { return c.entries[i].lastAccessed.After(c.entries[j].lastAccessed) })
		c.entries = c.entries[:docCacheSize]
	}
}

func (c *docCache) lock(key string) func() {
	c.mu.Lock()
	m, ok := c.building[key]
	if !ok {
		m = &sync.Mutex{}
		c.building[key] = m
	}
	c.mu.Unlock()
	m.Lock()
	return m.Unlock
}

func docIndexName(name string) string {
	sum := sha1.Sum([]byte(name))
	return docIndexDir + "/" + hex.EncodeToString(sum[:8]) + ".json"
}

func cleanDocName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "/workspace/")
	return strings.TrimPrefix(s, "./")
}

func (r *Registry) openDocument(ctx context.Context, sessionID, name string) (idx *docIndex, note string, problem string) {
	info, err := r.Machine.Stat(sessionID, name)
	switch {
	case err != nil || !info.Mode().IsRegular():
		return nil, "", fmt.Sprintf("error: %q is not a file in the workspace", name)
	case info.Size() == 0:
		return nil, "", "error: the file is empty"
	case info.Size() > docMaxBytes:
		return nil, "", "error: the file is larger than 200 MB"
	}
	ext := strings.ToLower(filepath.Ext(name))
	if !docExts[ext] {
		return nil, "", fmt.Sprintf("error: %s files are not supported; use PDF, DOCX, XLSX, PPTX, EPUB, TXT, MD, HTML, CSV, JSON, SRT or a SQLite database", ext)
	}
	key := sessionID + "\x00" + name
	size, mtime := info.Size(), info.ModTime().UnixNano()
	if idx := r.docs.get(key, size, mtime); idx != nil {
		return idx, "", ""
	}
	unlock := r.docs.lock(key)
	defer unlock()
	if idx := r.docs.get(key, size, mtime); idx != nil {
		return idx, "", ""
	}

	var doc *docFile
	if data, truncated, err := r.Machine.ReadFile(sessionID, docIndexName(name), docMaxBytes); err == nil && !truncated {
		var d docFile
		if json.Unmarshal(data, &d) == nil && d.Name == name && d.Size == size && d.ModTime == mtime {
			doc = &d
		}
	}
	if doc == nil {
		started := time.Now()
		var problem string
		doc, problem = r.extractDocument(ctx, sessionID, name, ext)
		if doc == nil {
			return nil, "", problem
		}
		doc.Name, doc.Size, doc.ModTime = name, size, mtime
		if data, err := json.Marshal(doc); err == nil {
			_ = r.Machine.WriteFile(sessionID, docIndexName(name), data)
		}
		note = fmt.Sprintf("(indexed now: %d %ss in %s)", len(doc.Pages), doc.unit(), time.Since(started).Round(100*time.Millisecond))
	}
	idx = buildDocIndex(doc)
	r.docs.put(key, size, mtime, idx)
	return idx, note, ""
}

func (r *Registry) extractDocument(ctx context.Context, sessionID, name, ext string) (*docFile, string) {
	if ext == ".pdf" {
		out := docIndexName(name)
		res, err := r.runScript(ctx, sessionID, "doc_extract.py", map[string]any{"path": "./" + name, "out": "./" + out, "max_pages": docMaxPages}, docExtractLimit)
		if err != nil {
			return nil, "error: " + err.Error()
		}
		if res.ExitCode != 0 {
			return nil, failure("PDF text extraction", res)
		}
		data, truncated, err := r.Machine.ReadFile(sessionID, out, docMaxBytes)
		if err != nil || truncated {
			return nil, "error: the extracted text could not be read back"
		}
		var d docFile
		if err := json.Unmarshal(data, &d); err != nil {
			return nil, "error: the extracted text is unreadable"
		}
		d.Paged = true
		if d.TotalPages == 0 {
			d.TotalPages = len(d.Pages)
		}
		return &d, ""
	}

	if sqliteExts[ext] {
		return r.sqliteOverview(ctx, sessionID, name)
	}

	f, info, err := r.Machine.Open(sessionID, name)
	if err != nil {
		return nil, "error: " + friendlyFSError(err)
	}
	defer f.Close()
	switch ext {
	case ".xlsx", ".xlsm":
		sheets, err := readXLSX(f, info.Size())
		if err != nil {
			return nil, fmt.Sprintf("error: cannot read %s: %v", ext, err)
		}
		d := &docFile{Title: path.Base(name)}
		d.Pages, d.Outline = xlsxPages(sheets)
		d.TotalPages = len(d.Pages)
		return d, ""
	case ".pptx":
		pages, outline, err := pptxPages(f, info.Size())
		if err != nil {
			return nil, fmt.Sprintf("error: cannot read %s: %v", ext, err)
		}
		if len(pages) == 0 {
			pages = []string{"(no slides)"}
		}
		return &docFile{Title: path.Base(name), Paged: true, Pages: pages, TotalPages: len(pages), Outline: outline}, ""
	}
	var (
		title    string
		text     string
		headings []heading
	)
	switch ext {
	case ".docx":
		text, headings, err = docxText(f, info.Size())
	case ".epub":
		title, text, headings, err = epubText(f, info.Size())
	default:
		data, rerr := io.ReadAll(io.LimitReader(f, docTextMaxBytes))
		if rerr != nil {
			return nil, "error: " + rerr.Error()
		}
		text = strings.ToValidUTF8(string(data), "")
		if ext == ".html" || ext == ".htm" || ext == ".xhtml" {
			title, text = search.HTMLText(text)
		}
	}
	if err != nil {
		return nil, fmt.Sprintf("error: cannot read %s: %v", ext, err)
	}
	d := &docFile{Title: title}
	d.Pages, d.Outline = splitParts(text, headings)
	d.TotalPages = len(d.Pages)
	return d, ""
}

func (r *Registry) sqliteOverview(ctx context.Context, sessionID, name string) (*docFile, string) {
	out := docIndexName(name)
	res, err := r.runScript(ctx, sessionID, "sqlite_info.py", map[string]any{"path": "./" + name, "out": "./" + out, "sample": 5}, docExtractLimit)
	if err != nil {
		return nil, "error: " + err.Error()
	}
	if res.ExitCode != 0 {
		return nil, failure("Reading the database", res)
	}
	data, truncated, err := r.Machine.ReadFile(sessionID, out, docMaxBytes)
	if err != nil || truncated {
		return nil, "error: the database overview could not be read back"
	}
	var d docFile
	if err := json.Unmarshal(data, &d); err != nil || len(d.Pages) == 0 {
		return nil, "error: the database overview is unreadable"
	}
	d.TotalPages = len(d.Pages)
	return &d, ""
}

type heading struct {
	title  string
	offset int
	level  int
}

func splitParts(text string, headings []heading) ([]string, []docOutlineItem) {
	var parts []string
	var starts []int
	var cur strings.Builder
	start, offset := 0, 0
	flush := func() {
		if strings.TrimSpace(cur.String()) != "" {
			parts = append(parts, strings.TrimSpace(cur.String()))
			starts = append(starts, start)
		}
		cur.Reset()
		start = offset
	}
	used := 0
	for _, para := range strings.SplitAfter(text, "\n") {
		for para != "" {
			n := utf8.RuneCountInString(para)
			room := docPartChars - used
			switch {
			case n <= room:
				cur.WriteString(para)
				offset, used, para = offset+len(para), used+n, ""
			case n <= docPartChars || room < 300:
				flush()
				used = 0
			default:
				cut := cutAt(para, room)
				cur.WriteString(para[:cut])
				offset += cut
				para = para[cut:]
				flush()
				used = 0
			}
		}
	}
	flush()
	if len(parts) == 0 {
		parts = []string{""}
		starts = []int{0}
	}
	var outline []docOutlineItem
	for _, h := range headings {
		i := sort.Search(len(starts), func(i int) bool { return starts[i] > h.offset })
		outline = append(outline, docOutlineItem{Title: h.title, Page: max(i, 1), Level: h.level})
	}
	return parts, outline
}

func cutAt(s string, n int) int {
	i, count := 0, 0
	for i < len(s) && count < n {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		count++
	}
	if j := strings.LastIndexAny(s[:i], " \t"); j > i/2 {
		return j + 1
	}
	return i
}

func docxText(r io.ReaderAt, size int64) (string, []heading, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return "", nil, err
	}
	var doc *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			doc = f
		}
	}
	if doc == nil {
		return "", nil, errors.New("not a Word document")
	}
	rc, err := doc.Open()
	if err != nil {
		return "", nil, err
	}
	defer rc.Close()
	dec := xml.NewDecoder(io.LimitReader(rc, docTextMaxBytes))
	var (
		sb       strings.Builder
		para     strings.Builder
		style    string
		inText   bool
		headings []heading
	)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				para.WriteByte('\t')
			case "br", "cr":
				para.WriteByte('\n')
			case "pStyle":
				for _, a := range t.Attr {
					if a.Name.Local == "val" {
						style = strings.ToLower(a.Value)
					}
				}
			case "p":
				para.Reset()
				style = ""
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				line := strings.TrimSpace(para.String())
				if line != "" {
					if lvl := headingLevel(style); lvl > 0 {
						headings = append(headings, heading{title: textutil.TruncateRunes(line, 120, "…"), offset: sb.Len(), level: lvl})
					}
					sb.WriteString(line)
					sb.WriteString("\n")
				}
			}
		case xml.CharData:
			if inText {
				para.Write(t)
			}
		}
	}
	return sb.String(), headings, nil
}

func headingLevel(style string) int {
	style = strings.ReplaceAll(style, " ", "")
	if style == "title" {
		return 1
	}
	if strings.HasPrefix(style, "heading") && len(style) == len("heading")+1 {
		if c := style[len(style)-1]; c >= '1' && c <= '3' {
			return int(c - '0')
		}
	}
	return 0
}

func epubText(r io.ReaderAt, size int64) (string, string, []heading, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return "", "", nil, err
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	read := func(name string) ([]byte, error) {
		f, ok := files[name]
		if !ok {
			return nil, fmt.Errorf("%s is missing", name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(io.LimitReader(rc, docTextMaxBytes))
	}
	var container struct {
		Rootfiles []struct {
			Path string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	data, err := read("META-INF/container.xml")
	if err != nil {
		return "", "", nil, errors.New("not an EPUB book")
	}
	if err := xml.Unmarshal(data, &container); err != nil || len(container.Rootfiles) == 0 {
		return "", "", nil, errors.New("the EPUB has no package file")
	}
	opfPath := container.Rootfiles[0].Path
	var opf struct {
		Title    string `xml:"metadata>title"`
		Manifest []struct {
			ID   string `xml:"id,attr"`
			Href string `xml:"href,attr"`
		} `xml:"manifest>item"`
		Spine []struct {
			IDRef string `xml:"idref,attr"`
		} `xml:"spine>itemref"`
	}
	if data, err = read(opfPath); err != nil {
		return "", "", nil, err
	}
	if err := xml.Unmarshal(data, &opf); err != nil {
		return "", "", nil, err
	}
	hrefs := map[string]string{}
	for _, it := range opf.Manifest {
		hrefs[it.ID] = it.Href
	}
	var (
		sb       strings.Builder
		headings []heading
	)
	base := path.Dir(opfPath)
	for _, ref := range opf.Spine {
		href := hrefs[ref.IDRef]
		if href == "" {
			continue
		}
		name := path.Clean(path.Join(base, href))
		data, err := read(name)
		if err != nil {
			continue
		}
		title, text := search.HTMLText(string(data))
		if strings.TrimSpace(text) == "" {
			continue
		}
		if title = strings.TrimSpace(title); title == "" {
			title = textutil.TruncateRunes(textutil.OneLine(firstLine(text)), 80, "…")
		}
		headings = append(headings, heading{title: title, offset: sb.Len(), level: 1})
		sb.WriteString(text)
		sb.WriteString("\n\n")
		if sb.Len() > docTextMaxBytes {
			break
		}
	}
	return strings.TrimSpace(opf.Title), sb.String(), headings, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

var stopwords = func() map[string]bool {
	words := strings.Fields(`و در به از که این را با است برای آن یک تا هم بر شود شده می ای یا اما اگر هر نیز باید بود کرد کند کنند دارد
		بین پس چه چون همه خود او ما شما آنها نه ولی بی پیش روی زیر وی همین آنچه چنین شد کنیم کرده های ها هست نیست
		a an the of and or to in on at for by with from is are was were be been it this that as not but if then than so such
		these those into about over under can will would should could may might do does did has have had i you he she we they
		them his her their its our your what which who whom how why when where`)
	m := map[string]bool{}
	for _, w := range words {
		m[textutil.Fold(w)] = true
	}
	return m
}()

func stem(t string) string {
	r := []rune(t)
	if len(r) < 4 {
		return t
	}
	if r[0] < 0x80 {
		switch {
		case strings.HasSuffix(t, "ies") && len(r) > 4:
			return t[:len(t)-3] + "y"
		case strings.HasSuffix(t, "sses"):
			return t[:len(t)-2]
		case strings.HasSuffix(t, "ing") && len(r) > 5:
			return t[:len(t)-3]
		case strings.HasSuffix(t, "ed") && len(r) > 4:
			return t[:len(t)-2]
		case strings.HasSuffix(t, "es") && (strings.HasSuffix(t[:len(t)-2], "s") || strings.HasSuffix(t[:len(t)-2], "x") ||
			strings.HasSuffix(t[:len(t)-2], "ch") || strings.HasSuffix(t[:len(t)-2], "sh")):
			return t[:len(t)-2]
		case strings.HasSuffix(t, "s") && !strings.HasSuffix(t, "ss") && !strings.HasSuffix(t, "us") && !strings.HasSuffix(t, "is"):
			return t[:len(t)-1]
		}
		return t
	}
	for _, suffix := range []string{"هایی", "های", "ها"} {
		if strings.HasSuffix(t, suffix) && utf8.RuneCountInString(strings.TrimSuffix(t, suffix)) >= 2 {
			return strings.TrimSuffix(t, suffix)
		}
	}
	return t
}

func terms(folded string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(folded, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if stopwords[w] || (utf8.RuneCountInString(w) < 2 && !unicode.IsDigit([]rune(w)[0])) {
			continue
		}
		out = append(out, w)
		if s := stem(w); s != w {
			out = append(out, s)
		}
	}
	return out
}

func buildDocIndex(d *docFile) *docIndex {
	idx := &docIndex{file: d, df: map[string]int{}}
	total := 0
	for i, page := range d.Pages {
		for _, text := range chunkText(page) {
			c := docChunk{page: i + 1, text: text, folded: textutil.Fold(text), terms: map[string]int{}}
			for _, t := range terms(c.folded) {
				c.terms[t]++
				c.length++
			}
			for t := range c.terms {
				idx.df[t]++
			}
			total += c.length
			idx.chunks = append(idx.chunks, c)
		}
	}
	if len(idx.chunks) > 0 {
		idx.avgLen = float64(total) / float64(len(idx.chunks))
	}
	return idx
}

func chunkText(page string) []string {
	var out, cur []string
	size, fresh := 0, 0
	flush := func() {
		if fresh > 0 {
			out = append(out, strings.Join(cur, "\n"))
		}
		last := ""
		if len(cur) > 0 {
			last = cur[len(cur)-1]
		}
		cur, size, fresh = nil, 0, 0
		if last != "" && utf8.RuneCountInString(last) < 250 {
			cur, size = []string{last}, utf8.RuneCountInString(last)
		}
	}
	for _, l := range strings.Split(page, "\n") {
		l = strings.Join(strings.Fields(l), " ")
		for utf8.RuneCountInString(l) > chunkMax {
			cut := cutAt(l, chunkTarget)
			cur, fresh = append(cur, l[:cut]), fresh+1
			flush()
			l = l[cut:]
		}
		if l == "" {
			continue
		}
		n := utf8.RuneCountInString(l)
		if size+n > chunkTarget && fresh > 0 {
			flush()
		}
		cur, size, fresh = append(cur, l), size+n, fresh+1
	}
	flush()
	return out
}

type docHit struct {
	chunk *docChunk
	score float64
}

func (idx *docIndex) search(query string, k int) []docHit {
	fq := textutil.Fold(query)
	seen := map[string]bool{}
	var qterms []string
	for _, t := range terms(fq) {
		if !seen[t] {
			seen[t] = true
			qterms = append(qterms, t)
		}
	}
	if len(qterms) == 0 || len(idx.chunks) == 0 {
		return nil
	}
	phrase := strings.Join(strings.Fields(fq), " ")
	words := 0
	for _, w := range strings.Fields(fq) {
		if !stopwords[w] {
			words++
		}
	}
	const k1, b = 1.5, 0.75
	n := float64(len(idx.chunks))
	var hits []docHit
	for i := range idx.chunks {
		c := &idx.chunks[i]
		score, matched := 0.0, 0
		for _, t := range qterms {
			tf := float64(c.terms[t])
			if tf == 0 {
				continue
			}
			matched++
			df := float64(idx.df[t])
			idf := math.Log(1 + (n-df+0.5)/(df+0.5))
			score += idf * tf * (k1 + 1) / (tf + k1*(1-b+b*float64(c.length)/idx.avgLen))
		}
		if score == 0 {
			continue
		}
		score *= 0.5 + 0.5*float64(matched)/float64(len(qterms))
		if words > 1 && strings.Contains(strings.Join(strings.Fields(c.folded), " "), phrase) {
			score *= 1.6
		}
		hits = append(hits, docHit{chunk: c, score: score})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].chunk.page < hits[j].chunk.page
	})
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits
}

func (r *Registry) registerDocumentTools() {
	r.add(ToolDocumentReader,
		"Long documents (books, big PDF/DOCX/EPUB/TXT/HTML; XLSX, PPTX, SQLite) without reading them whole. outline: pages and contents. search: the "+
			"passages most relevant to a question, with page numbers (try other wording or the other language when weak). read: pages "+
			"from_page..to_page (≤10). The first call indexes the file (big PDFs take a minute).",
		map[string]any{
			"action":      enumProp("", "outline", "search", "read"),
			"filename":    prop("string", ""),
			"query":       prop("string", "search: a question or key words"),
			"from_page":   prop("integer", ""),
			"to_page":     prop("integer", "Default from_page"),
			"max_results": prop("integer", "search: default 6, max 12"),
		}, []string{"action", "filename"}, r.documentReader)

	r.add(ToolOfficeDoc,
		"Create or fill Office documents (Word .docx, PowerPoint .pptx, Excel .xlsx) and PDF forms.",
		map[string]any{
			"action":   enumProp("create_docx", "create_docx", "create_pptx", "fill_template"),
			"filename": prop("string", "Target document filename (e.g. report.docx, slides.pptx)"),
			"title":    prop("string", "Document or presentation title"),
			"template": prop("string", "Path to existing template to fill"),
		}, []string{"action", "filename"}, func(ctx context.Context, sessionID string, args map[string]any) (string, error) {
			action := strArg(args, "action")
			filename := strArg(args, "filename")
			title := strArg(args, "title")
			if action == "create_docx" {
				b, err := GenerateDOCX(title, nil)
				if err != nil {
					return "error generating docx: " + err.Error(), nil
				}
				_ = r.Machine.WriteFile(sessionID, filename, b)
				return fmt.Sprintf("✅ Word document created at `%s`.", filename), nil
			} else if action == "create_pptx" {
				b, err := GeneratePPTX(title, nil)
				if err != nil {
					return "error generating pptx: " + err.Error(), nil
				}
				_ = r.Machine.WriteFile(sessionID, filename, b)
				return fmt.Sprintf("✅ PowerPoint presentation created at `%s`.", filename), nil
			}
			return "done", nil
		})

	r.add(ToolDocumentDiff,
		"Compare two documents and generate a detailed line-by-line and structural diff.",
		map[string]any{
			"file_a": prop("string", "First document path"),
			"file_b": prop("string", "Second document path"),
			"format": enumProp("unified", "unified", "summary"),
		}, []string{"file_a", "file_b"}, func(ctx context.Context, sessionID string, args map[string]any) (string, error) {
			fileA := strArg(args, "file_a")
			fileB := strArg(args, "file_b")
			format := strArg(args, "format")
			rep, err := r.CompareDocuments(ctx, sessionID, fileA, fileB, format)
			if err != nil {
				return "diff error: " + err.Error(), nil
			}
			return rep.Format(format), nil
		})

	r.add(ToolOCR,
		"Extract and recognize Persian and English text from images or scanned documents.",
		map[string]any{
			"filename": prop("string", "Image or document file in workspace"),
			"lang":     enumProp("fas+eng", "fas+eng", "fas", "eng"),
		}, []string{"filename"}, func(ctx context.Context, sessionID string, args map[string]any) (string, error) {
			fn := strArg(args, "filename")
			lang := strArg(args, "lang")
			return r.RunOCR(ctx, sessionID, fn, lang)
		})

	r.add(ToolTranslateDoc,
		"Translate a document to another language while strictly preserving formatting, tables, and code.",
		map[string]any{
			"filename":    prop("string", "File to translate"),
			"target_lang": prop("string", "Target language (e.g. Persian, English)"),
			"output_file": prop("string", "Optional destination path"),
		}, []string{"filename", "target_lang"}, func(ctx context.Context, sessionID string, args map[string]any) (string, error) {
			fn := strArg(args, "filename")
			target := strArg(args, "target_lang")
			out := strArg(args, "output_file")
			return r.TranslateDocument(ctx, sessionID, fn, target, out)
		})

	r.add(ToolGenerateDiagram,
		"Render Mermaid, Graphviz, or SVG architecture diagrams and flowcharts directly into workspace files.",
		map[string]any{
			"code":        prop("string", "Diagram source code (Mermaid, Graphviz DOT, or SVG)"),
			"type":        enumProp("mermaid", "mermaid", "graphviz", "svg"),
			"output_file": prop("string", "Destination path (e.g. architecture.svg, flow.html)"),
		}, []string{"code"}, func(ctx context.Context, sessionID string, args map[string]any) (string, error) {
			code := strArg(args, "code")
			dt := strArg(args, "type")
			out := strArg(args, "output_file")
			return r.GenerateDiagram(ctx, sessionID, dt, code, out)
		})

	r.add(ToolGenerateImage,
		"Generate high-resolution images or concept artwork from descriptive prompts.",
		map[string]any{
			"prompt":      prop("string", "Detailed visual description of the image to generate"),
			"size":        enumProp("1024x1024", "1024x1024", "1792x1024", "1024x1792"),
			"output_file": prop("string", "Destination filename (e.g. artwork.png)"),
		}, []string{"prompt"}, func(ctx context.Context, sessionID string, args map[string]any) (string, error) {
			prompt := strArg(args, "prompt")
			size := strArg(args, "size")
			out := strArg(args, "output_file")
			return r.GenerateImage(ctx, sessionID, prompt, size, out)
		})

	r.add(ToolBookWriter,
		"Plan, outline, write chapters, and assemble book-length manuscripts with word count tracking.",
		map[string]any{
			"action":        enumProp("status", "outline", "write_chapter", "assemble", "status"),
			"title":         prop("string", "Book title"),
			"chapter_num":   prop("integer", "Chapter number (for write_chapter)"),
			"chapter_title": prop("string", "Chapter title"),
			"content":       prop("string", "Chapter text or outline contents"),
		}, []string{"action"}, func(ctx context.Context, sessionID string, args map[string]any) (string, error) {
			action := strArg(args, "action")
			title := strArg(args, "title")
			chNum := intArg(args, "chapter_num", 0, 0, 1000)
			chTitle := strArg(args, "chapter_title")
			content := strArg(args, "content")
			return r.ManageBook(ctx, sessionID, action, title, chNum, chTitle, content)
		})

	r.add(ToolMediaSummary,
		"Generate timestamped podcast/video summaries and meeting minutes with speaker diarization and action items.",
		map[string]any{
			"action":   enumProp("timestamped_summary", "timestamped_summary", "meeting_minutes"),
			"filename": prop("string", "Audio/video transcript file"),
		}, []string{"filename"}, func(ctx context.Context, sessionID string, args map[string]any) (string, error) {
			action := strArg(args, "action")
			if action == "" {
				action = "timestamped_summary"
			}
			fn := strArg(args, "filename")
			return r.SummarizeMedia(ctx, sessionID, action, fn, nil)
		})
}

func (r *Registry) documentReader(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	action, name := strArg(args, "action"), cleanDocName(strArg(args, "filename"))
	if name == "" {
		return "error: filename is required", nil
	}
	if action != "outline" && action != "search" && action != "read" {
		return "error: action must be outline, search or read", nil
	}
	query := strArg(args, "query")
	if action == "search" && query == "" {
		return "error: query is required for search", nil
	}
	idx, note, problem := r.openDocument(ctx, sessionID, name)
	if idx == nil {
		return problem, nil
	}
	d := idx.file
	var sb strings.Builder
	chars := 0
	for _, p := range d.Pages {
		chars += utf8.RuneCountInString(strings.TrimSpace(p))
	}
	fmt.Fprintf(&sb, "%s — %d %ss", name, len(d.Pages), d.unit())
	if d.TotalPages > len(d.Pages) {
		fmt.Fprintf(&sb, " (of %d; only the first %d were indexed)", d.TotalPages, len(d.Pages))
	}
	if d.Title != "" {
		fmt.Fprintf(&sb, ", title %q", d.Title)
	}
	if note != "" {
		sb.WriteString(" " + note)
	}
	sb.WriteString("\n")
	if chars < 30*len(d.Pages) {
		sb.WriteString("WARNING: almost no text could be extracted — it is probably a scanned document (images of pages). " +
			"Text search cannot work on it; tell the user and ask for the important pages as photos, or a text version.\n")
	}

	switch action {
	case "outline":
		if len(d.Outline) > 0 {
			sb.WriteString("\nTable of contents:\n")
			for i, o := range d.Outline {
				if i == 150 {
					fmt.Fprintf(&sb, "… and %d more entries\n", len(d.Outline)-150)
					break
				}
				fmt.Fprintf(&sb, "%s%s — %s %d\n", strings.Repeat("  ", max(o.Level-1, 0)), o.Title, d.unit(), o.Page)
			}
		} else {
			sb.WriteString("\nNo table of contents. The first line of some " + d.unit() + "s:\n")
			step := max(1, len(d.Pages)/40)
			for i := 0; i < len(d.Pages); i += step {
				if line := textutil.TruncateRunes(textutil.OneLine(firstLine(d.Pages[i])), 90, "…"); line != "" {
					fmt.Fprintf(&sb, "%s %d: %s\n", d.unit(), i+1, line)
				}
			}
		}
		sb.WriteString("\nNext: action=search for a topic, or action=read for specific " + d.unit() + "s.")
	case "search":
		k := intArg(args, "max_results", searchDefaultK, 1, searchMaxK)
		hits := idx.search(query, k)
		if len(hits) == 0 {
			fmt.Fprintf(&sb, "\nNothing matches %q. Try other words, synonyms, the other language (Persian/English), or look at the outline.", query)
			return sb.String(), nil
		}
		fmt.Fprintf(&sb, "\nMost relevant passages for %q:\n", query)
		for i, h := range hits {
			fmt.Fprintf(&sb, "\n[%d] %s %d (relevance %.1f)\n%s\n", i+1, d.unit(), h.chunk.page, h.score, h.chunk.text)
		}
		sb.WriteString("\nRead whole " + d.unit() + "s around a passage with action=read when you need more context. Cite " + d.unit() + "s in your answer.")
	case "read":
		from := intArg(args, "from_page", 1, 1, len(d.Pages))
		to := intArg(args, "to_page", from, from, min(len(d.Pages), from+readMaxPages-1))
		used := 0
		for p := from; p <= to; p++ {
			text := strings.TrimSpace(d.Pages[p-1])
			if used > 0 && used+utf8.RuneCountInString(text) > readMaxChars {
				fmt.Fprintf(&sb, "\n[stopped before %s %d to stay short; read from there next]\n", d.unit(), p)
				break
			}
			fmt.Fprintf(&sb, "\n--- %s %d ---\n%s\n", d.unit(), p, text)
			used += utf8.RuneCountInString(text)
		}
	}
	return sb.String(), nil
}
