// Package skills keeps procedural knowledge on disk: how to do a kind of job,
// written as rules, not facts. A skill is a folder with a SKILL.md (a short
// front matter and the instructions) and optional files beside it
// (references/, templates/, scripts/):
//
//	<root>/<category>/<name>/SKILL.md
//
// The system prompt carries only an index (each name with a one-line
// description); the model reads a whole skill, or one of its files, when the
// task needs it. The skills live outside the workspaces, which are emptied
// automatically.
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
	// MaxDescription is the longest description, in characters.
	MaxDescription = 200
	// MaxBody bounds a SKILL.md.
	MaxBody = 256 << 10
	// MaxFileRead bounds what ReadFile returns of one file.
	MaxFileRead = 200 << 10
	// IndexLimit is the most skills listed one by one in the index; above
	// it, the index lists the categories and how many skills each has.
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

// ValidName reports whether s can name a skill or a category.
func ValidName(s string) bool { return nameRe.MatchString(s) }

// NormalizeName lowercases a name and turns spaces into dashes ("Linux Ops" → "linux-ops").
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
	Files       []string // reference, template and script files, relative to Dir
	Enabled     bool
	Uses        int
	UpdatedAt   time.Time
}

type Store struct {
	mu     sync.RWMutex
	dir    string
	list   []Skill // by category, then name; disabled ones too
	byName map[string]*Skill
	index  string
	on     int
	uses   map[string]int

	sig     string       // what the folder looked like when it was loaded
	checked atomic.Int64 // when the folder was last looked at (unix nano)
}

// New opens the skills folder (creating it when missing) and loads it.
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

// Dir is the skills folder.
func (s *Store) Dir() string { return s.dir }

// Reload reads every skill from disk again. A broken skill is skipped (and
// logged); it never stops the others from loading.
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

// attachments lists the files beside SKILL.md (no hidden files, no symlinks).
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

// privateNote ends the index: the skills are for the work, not for the user.
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

// recheckEvery is how often the folder is looked at for skills written by
// hand (by the owner, or by the agent through a shell).
const recheckEvery = 2 * time.Second

// dirSignature says what the skill files look like: any skill added, removed
// or edited changes it.
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

// refresh reads the folder again when a skill file changed on disk. It looks
// at most every recheckEvery, so asking for the index on every request is cheap.
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

// Len is the number of enabled skills (the ones in the index).
func (s *Store) Len() int {
	s.refresh()
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.on
}

// Index is the block for the system prompt ("" when there is no enabled skill).
// It is built when the skills load, never per message.
func (s *Store) Index() string {
	s.refresh()
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.index
}

// Get finds a skill by name, enabled or not.
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

// Snapshot lists every skill (disabled ones too), by category and name.
func (s *Store) Snapshot() []Skill {
	s.refresh()
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Skill, len(s.list))
	copy(out, s.list)
	return out
}

// Touch counts one use of a skill; the counts survive restarts.
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

// ReadFile returns one file of a skill. rel must stay inside the skill
// folder; symlinks are never followed. A long file is cut at MaxFileRead.
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

// Save creates a skill or replaces the description and instructions of an
// existing one (its patch version goes up: 1.0.0 → 1.0.1). An empty category
// keeps the current one ("general" for a new skill).
func (s *Store) Save(name, description, body, category string) error {
	name, category = NormalizeName(name), NormalizeName(category)
	description = textutil.OneLine(description)
	body = strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))
	if strings.HasPrefix(body, "---") {
		_, body = Parse(body) // the whole SKILL.md was sent as the body
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

// Import saves a whole SKILL.md (front matter and body), e.g. a file an admin
// uploaded; fallbackName names it when the front matter does not.
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

// Delete removes a skill folder.
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

// SetEnabled turns a skill on or off; a disabled skill leaves the index but
// stays on disk.
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
		_ = os.Remove(dir) // fails, as it should, when the folder still has something
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

// writeAtomic replaces a file through a temporary file in the same folder:
// a reader never sees half a file.
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

// ---------------------------------------------------------------- front matter

type field struct{ key, value string }

// frontMatter keeps the keys in their order; unknown keys survive a rewrite.
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

// Parse splits a SKILL.md into its front matter (the simple "key: value"
// lines between the first two "---" lines) and its body. It needs no YAML
// library: quoted values are unquoted, nested values are ignored.
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
	return nil, strings.TrimSpace(raw) // no closing "---": it was not front matter
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

// Render writes a SKILL.md: the front matter, then the body.
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
