package skills

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const (
	FileName        = "SKILL.md"
	DefaultCategory = "general"
	DefaultVersion  = "1.0.0"

	MaxDescription = 200

	MaxBody = 256 << 10

	MaxFileRead = 200 << 10

	IndexLimit = 80

	indexDescription = 160
	maxFiles         = 200
	usesFile         = ".uses.json"
	seededFile       = ".seeded"
)

var (
	ErrNotFound        = errors.New("no such skill")
	ErrBadPath         = errors.New("the file must be a path inside the skill folder")
	ErrBadName         = errors.New("invalid name: use lowercase letters, digits, '.', '_' and '-' (2 to 64 characters), e.g. linux-server-ops")
	ErrBadCategory     = errors.New("invalid category: lowercase letters, digits, '.', '_' and '-' (2 to 64 characters), e.g. devops")
	ErrNoDescription   = errors.New("the description is required: one line saying when to use the skill")
	ErrLongDescription = fmt.Errorf("the description is longer than %d characters", MaxDescription)
	ErrEmptyBody       = errors.New("the instructions (body) are empty")
	ErrBigBody         = fmt.Errorf("the instructions are larger than %d KB", MaxBody>>10)

	nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,63}$`)
)

func ValidName(s string) bool { return nameRe.MatchString(s) }

func NormalizeName(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), "-")
}

type Skill struct {
	Name        string
	Description string
	Category    string
	Version     string
	Body        string
	Dir         string
	Files       []string
	Enabled     bool
	Uses        int
	UpdatedAt   time.Time
}

type Store struct {
	mu     sync.RWMutex
	dir    string
	list   []Skill
	byName map[string]*Skill
	index  string
	on     int
	uses   map[string]int

	sig     string
	checked atomic.Int64
}

func New(dir string) (*Store, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("skills folder: %w", err)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	s := &Store{dir: abs, byName: map[string]*Skill{}, uses: map[string]int{}}
	if data, err := os.ReadFile(filepath.Join(abs, usesFile)); err == nil {
		_ = json.Unmarshal(data, &s.uses)
	}
	return s, s.Reload()
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reloadLocked()
}

func (s *Store) reloadLocked() error {
	s.sig = dirSignature(s.dir)
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("skills folder: %w", err)
	}
	var list []Skill
	add := func(dir, category string) {
		sk, err := loadSkill(dir, category)
		if err != nil {
			log.Printf("skills: skipping %s: %v", strings.TrimPrefix(dir, s.dir+string(filepath.Separator)), err)
			return
		}
		list = append(list, sk)
	}
	for _, e := range entries {
		if !plainDir(e) {
			continue
		}
		top := filepath.Join(s.dir, e.Name())
		if isFile(filepath.Join(top, FileName)) {
			add(top, DefaultCategory)
			continue
		}
		subs, err := os.ReadDir(top)
		if err != nil {
			continue
		}
		for _, sub := range subs {
			if dir := filepath.Join(top, sub.Name()); plainDir(sub) && isFile(filepath.Join(dir, FileName)) {
				add(dir, e.Name())
			}
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Category != list[j].Category {
			return list[i].Category < list[j].Category
		}
		return list[i].Name < list[j].Name
	})
	byName := make(map[string]*Skill, len(list))
	kept := list[:0]
	for _, sk := range list {
		if prev, dup := byName[sk.Name]; dup {
			log.Printf("skills: %q is defined twice (%s and %s); keeping the first", sk.Name, prev.Dir, sk.Dir)
			continue
		}
		sk.Uses = s.uses[sk.Name]
		kept = append(kept, sk)
		byName[sk.Name] = &kept[len(kept)-1]
	}
	s.list, s.byName = kept, byName
	s.on = 0
	for _, sk := range kept {
		if sk.Enabled {
			s.on++
		}
	}
	s.index = buildIndex(kept, s.on)
	return nil
}

func plainDir(e fs.DirEntry) bool {
	return e.IsDir() && e.Type()&fs.ModeSymlink == 0 && !strings.HasPrefix(e.Name(), ".")
}

func isFile(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode().IsRegular()
}

func loadSkill(dir, category string) (Skill, error) {
	p := filepath.Join(dir, FileName)
	fi, err := os.Lstat(p)
	if err != nil {
		return Skill{}, err
	}
	if fi.Size() > MaxBody {
		return Skill{}, fmt.Errorf("%s is larger than %d KB", FileName, MaxBody>>10)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return Skill{}, err
	}
	if !utf8.Valid(raw) {
		return Skill{}, fmt.Errorf("%s is not UTF-8 text", FileName)
	}
	meta, body := Parse(string(raw))
	sk := Skill{
		Name:        NormalizeName(meta.get("name")),
		Description: textutil.OneLine(meta.get("description")),
		Category:    NormalizeName(meta.get("category")),
		Version:     meta.get("version"),
		Body:        body,
		Dir:         dir,
		Enabled:     true,
		UpdatedAt:   fi.ModTime(),
	}
	if sk.Name == "" {
		sk.Name = NormalizeName(filepath.Base(dir))
	}
	if !ValidName(sk.Name) {
		return Skill{}, fmt.Errorf("invalid name %q (lowercase letters, digits, '.', '_' and '-'; 2 to 64 characters)", sk.Name)
	}
	if sk.Description == "" {
		return Skill{}, errors.New("no description in the front matter")
	}
	if utf8.RuneCountInString(sk.Description) > MaxDescription {
		log.Printf("skills: the description of %q is longer than %d characters; it is shortened", sk.Name, MaxDescription)
		sk.Description = textutil.TruncateRunes(sk.Description, MaxDescription, "…")
	}
	if !ValidName(sk.Category) {
		sk.Category = NormalizeName(category)
		if !ValidName(sk.Category) {
			sk.Category = DefaultCategory
		}
	}
	if sk.Version == "" {
		sk.Version = DefaultVersion
	}
	if v := meta.get("enabled"); v != "" {
		if on, err := strconv.ParseBool(v); err == nil {
			sk.Enabled = on
		}
	}
	sk.Files = attachments(dir)
	return sk, nil
}

func attachments(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if strings.HasPrefix(d.Name(), ".") || d.Type()&fs.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if strings.Count(rel, string(filepath.Separator)) >= 2 {
				return filepath.SkipDir
			}
			return nil
		}
		if rel != FileName && d.Type().IsRegular() && len(out) < maxFiles {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

const privateNote = "Skills are private: never name or list them to the user."

func buildIndex(list []Skill, on int) string {
	if on == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<available_skills>\n")
	if on > IndexLimit {
		counts := map[string]int{}
		var cats []string
		for _, sk := range list {
			if sk.Enabled {
				if counts[sk.Category] == 0 {
					cats = append(cats, sk.Category)
				}
				counts[sk.Category]++
			}
		}
		for _, c := range cats {
			fmt.Fprintf(&sb, "  %s: %d skills\n", c, counts[c])
		}
		fmt.Fprintf(&sb, "</available_skills>\nThere are %d skills, too many to list here: call list_skills with a category to see its skills. "+
			"When a job of several steps is one a skill covers, call read_skill first and follow it. "+privateNote, on)
		return sb.String()
	}
	cat := ""
	for _, sk := range list {
		if !sk.Enabled {
			continue
		}
		if sk.Category != cat {
			cat = sk.Category
			fmt.Fprintf(&sb, "  %s:\n", cat)
		}
		desc := strings.NewReplacer("<", "‹", ">", "›").Replace(textutil.TruncateRunes(sk.Description, indexDescription, "…"))
		fmt.Fprintf(&sb, "    - %s: %s\n", sk.Name, desc)
	}
	sb.WriteString("</available_skills>\nWhen a job of several steps is one a skill covers, call read_skill first and follow it. " + privateNote)
	return sb.String()
}

const recheckEvery = 2 * time.Second

func dirSignature(dir string) string {
	var n int
	var newest time.Time
	var size int64
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if rel, e := filepath.Rel(dir, path); e == nil && strings.Count(rel, string(filepath.Separator)) >= 3 {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != FileName {
			return nil
		}
		if fi, e := d.Info(); e == nil {
			n++
			size += fi.Size()
			if fi.ModTime().After(newest) {
				newest = fi.ModTime()
			}
		}
		return nil
	})
	return fmt.Sprintf("%d/%d/%d", n, size, newest.UnixNano())
}

func (s *Store) refresh() {
	now := time.Now().UnixNano()
	last := s.checked.Load()
	if now-last < int64(recheckEvery) || !s.checked.CompareAndSwap(last, now) {
		return
	}
	s.mu.RLock()
	same := s.sig == dirSignature(s.dir)
	s.mu.RUnlock()
	if !same {
		if err := s.Reload(); err != nil {
			log.Printf("skills: %v", err)
		}
	}
}

func (s *Store) Len() int {
	s.refresh()
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.on
}

func (s *Store) Index() string {
	s.refresh()
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.index
}

func (s *Store) Get(name string) (Skill, bool) {
	s.refresh()
	s.mu.RLock()
	defer s.mu.RUnlock()
	sk, ok := s.byName[NormalizeName(name)]
	if !ok {
		return Skill{}, false
	}
	return *sk, true
}

func (s *Store) Snapshot() []Skill {
	s.refresh()
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Skill, len(s.list))
	copy(out, s.list)
	return out
}

func (s *Store) Touch(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sk, ok := s.byName[name]
	if !ok {
		return
	}
	s.uses[name]++
	sk.Uses = s.uses[name]
	data, err := json.Marshal(s.uses)
	if err == nil {
		err = writeAtomic(filepath.Join(s.dir, usesFile), data)
	}
	if err != nil {
		log.Printf("skills: saving the use counts: %v", err)
	}
}

func (s *Store) ReadFile(name, rel string) (string, error) {
	sk, ok := s.Get(name)
	if !ok {
		return "", ErrNotFound
	}
	rel = strings.TrimSpace(strings.ReplaceAll(rel, "\\", "/"))
	if rel == "" || strings.HasPrefix(rel, "/") || filepath.IsAbs(rel) {
		return "", ErrBadPath
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	sep := string(filepath.Separator)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+sep) {
		return "", ErrBadPath
	}
	full := filepath.Join(sk.Dir, clean)
	if !strings.HasPrefix(full, sk.Dir+sep) {
		return "", ErrBadPath
	}
	cur := sk.Dir
	for _, part := range strings.Split(clean, sep) {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if err != nil {
			return "", fmt.Errorf("no file %q in skill %s", filepath.ToSlash(clean), sk.Name)
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return "", ErrBadPath
		}
	}
	f, err := os.Open(full)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%q is not a file", filepath.ToSlash(clean))
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxFileRead+1))
	if err != nil {
		return "", err
	}
	cut := len(data) > MaxFileRead
	if cut {
		data = data[:MaxFileRead]
		for len(data) > 0 && !utf8.Valid(data) {
			data = data[:len(data)-1]
		}
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("%q is a binary file", filepath.ToSlash(clean))
	}
	out := string(data)
	if cut {
		out += fmt.Sprintf("\n… [cut: the file is longer than %d KB]", MaxFileRead>>10)
	}
	return out, nil
}

func (s *Store) Save(name, description, body, category string) error {
	name, category = NormalizeName(name), NormalizeName(category)
	description = textutil.OneLine(description)
	body = strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))
	if strings.HasPrefix(body, "---") {
		_, body = Parse(body)
	}
	switch {
	case !ValidName(name):
		return fmt.Errorf("%w (got %q)", ErrBadName, name)
	case description == "":
		return ErrNoDescription
	case utf8.RuneCountInString(description) > MaxDescription:
		return ErrLongDescription
	case body == "" || !utf8.ValidString(body):
		return ErrEmptyBody
	case len(body) > MaxBody-1024:
		return ErrBigBody
	case category != "" && !ValidName(category):
		return fmt.Errorf("%w (got %q)", ErrBadCategory, category)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	meta := frontMatter{}
	version, enabled, dir := DefaultVersion, true, ""
	if cur, ok := s.byName[name]; ok {
		dir, version, enabled = cur.Dir, bumpPatch(cur.Version), cur.Enabled
		if raw, err := os.ReadFile(filepath.Join(cur.Dir, FileName)); err == nil {
			meta, _ = Parse(string(raw))
		}
		if category == "" {
			category = cur.Category
		}
		if category != cur.Category {
			moved := filepath.Join(s.dir, category, name)
			if _, err := os.Lstat(moved); errors.Is(err, fs.ErrNotExist) && os.MkdirAll(filepath.Dir(moved), 0o755) == nil &&
				os.Rename(cur.Dir, moved) == nil {
				removeIfEmpty(filepath.Dir(cur.Dir), s.dir)
				dir = moved
			}
		}
	}
	if category == "" {
		category = DefaultCategory
	}
	if dir == "" {
		dir = filepath.Join(s.dir, category, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	meta.set("name", name)
	meta.set("description", description)
	meta.set("version", version)
	meta.set("category", category)
	if enabled {
		meta.del("enabled")
	} else {
		meta.set("enabled", "false")
	}
	if err := writeAtomic(filepath.Join(dir, FileName), []byte(Render(meta, body))); err != nil {
		return err
	}
	return s.reloadLocked()
}

func (s *Store) Import(raw, fallbackName string) (string, error) {
	raw = strings.TrimPrefix(raw, "\uFEFF")
	if !utf8.ValidString(raw) {
		return "", ErrEmptyBody
	}
	if len(raw) > MaxBody {
		return "", ErrBigBody
	}
	meta, body := Parse(raw)
	name := NormalizeName(meta.get("name"))
	if name == "" {
		name = NormalizeName(fallbackName)
	}
	if err := s.Save(name, meta.get("description"), body, meta.get("category")); err != nil {
		return "", err
	}
	return name, nil
}

func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sk, ok := s.byName[NormalizeName(name)]
	if !ok {
		return ErrNotFound
	}
	if !strings.HasPrefix(sk.Dir, s.dir+string(filepath.Separator)) {
		return ErrBadPath
	}
	if err := os.RemoveAll(sk.Dir); err != nil {
		return err
	}
	removeIfEmpty(filepath.Dir(sk.Dir), s.dir)
	return s.reloadLocked()
}

func (s *Store) SetEnabled(name string, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sk, ok := s.byName[NormalizeName(name)]
	if !ok {
		return ErrNotFound
	}
	p := filepath.Join(sk.Dir, FileName)
	raw, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	meta, body := Parse(string(raw))
	if len(meta) == 0 {
		meta.set("name", sk.Name)
		meta.set("description", sk.Description)
	}
	if on {
		meta.del("enabled")
	} else {
		meta.set("enabled", "false")
	}
	if err := writeAtomic(p, []byte(Render(meta, body))); err != nil {
		return err
	}
	return s.reloadLocked()
}

func removeIfEmpty(dir, root string) {
	if dir != root && strings.HasPrefix(dir, root+string(filepath.Separator)) {
		_ = os.Remove(dir)
	}
}

func bumpPatch(v string) string {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".")
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	n, err := strconv.Atoi(parts[2])
	if err != nil {
		return DefaultVersion
	}
	parts[2] = strconv.Itoa(n + 1)
	return strings.Join(parts[:3], ".")
}

func writeAtomic(p string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o644)
	}
	if err == nil {
		err = os.Rename(tmp, p)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

type field struct{ key, value string }

type frontMatter []field

func (m frontMatter) get(key string) string {
	for _, f := range m {
		if f.key == key {
			return f.value
		}
	}
	return ""
}

func (m *frontMatter) set(key, value string) {
	for i, f := range *m {
		if f.key == key {
			(*m)[i].value = value
			return
		}
	}
	*m = append(*m, field{key, value})
}

func (m *frontMatter) del(key string) {
	out := (*m)[:0]
	for _, f := range *m {
		if f.key != key {
			out = append(out, f)
		}
	}
	*m = out
}

func Parse(raw string) (frontMatter, string) {
	raw = strings.ReplaceAll(strings.TrimPrefix(raw, "\uFEFF"), "\r\n", "\n")
	lines := strings.Split(raw, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, strings.TrimSpace(raw)
	}
	var meta frontMatter
	for i := 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "---" {
			return meta, strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
		}
		if line == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		key = strings.ToLower(strings.TrimSpace(key))
		if !ok || key == "" {
			continue
		}
		meta.set(key, unquote(strings.TrimSpace(value)))
	}
	return nil, strings.TrimSpace(raw)
}

func unquote(v string) string {
	if len(v) >= 2 {
		switch q := v[0]; {
		case q == '"' && v[len(v)-1] == '"':
			return strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(v[1 : len(v)-1])
		case q == '\'' && v[len(v)-1] == '\'':
			return strings.ReplaceAll(v[1:len(v)-1], "''", "'")
		}
	}
	return v
}

func Render(meta frontMatter, body string) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	for _, f := range meta {
		v := f.value
		if f.key == "description" || strings.ContainsAny(v, ":#\"'") {
			v = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
		}
		fmt.Fprintf(&sb, "%s: %s\n", f.key, v)
	}
	sb.WriteString("---\n\n")
	sb.WriteString(strings.TrimSpace(body))
	sb.WriteString("\n")
	return sb.String()
}
