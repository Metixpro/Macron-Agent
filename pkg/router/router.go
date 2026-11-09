package router

import (
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Kind string

const (
	Chat      Kind = "chat"
	Quick     Kind = "quick"
	Media     Kind = "media"
	Writing   Kind = "writing"
	Vision    Kind = "vision"
	Code      Kind = "code"
	Research  Kind = "research"
	Reasoning Kind = "reasoning"
	Documents Kind = "documents"
	Project   Kind = "project"
)

var Kinds = []Kind{Chat, Quick, Media, Writing, Vision, Code, Research, Reasoning, Documents, Project}

var DefaultStrong = map[Kind]bool{Code: true, Research: true, Reasoning: true, Documents: true, Project: true}

type Sensitivity string

const (
	Cheap    Sensitivity = "cheap"
	Balanced Sensitivity = "balanced"
	Quality  Sensitivity = "quality"
)

type Input struct {
	Reply  bool
	Text   string
	Hint   string
	Images int
	Files  []string
	Deep   bool
}

type Verdict struct {
	Kind    Kind
	Hard    bool
	Signals []Signal
	Scores  map[Kind]int
}

type Signal struct {
	Why  string
	Arg  string
	Hint bool
}

func (s Signal) String() string {
	out := s.Why
	if s.Arg != "" {
		out += " " + s.Arg
	}
	if s.Hint {
		out += " (attached)"
	}
	return out
}

func Decide(v Verdict, strong map[Kind]bool, s Sensitivity) bool {
	switch {
	case strong[v.Kind]:
		return true
	case v.Hard && s != Cheap:
		return true
	case s == Quality:
		return v.Kind != Chat && v.Kind != Quick && v.Kind != Media
	}
	return false
}

type rule struct {
	kind   Kind
	weight int
	re     *regexp.Regexp
	why    string
}

func r(kind Kind, weight int, why, pattern string) rule {
	return rule{kind, weight, regexp.MustCompile(`(?i)` + pattern), why}
}

var rules = []rule{
	r(Code, 4, "code", "```|\\bfunc |\\bdef |\\bclass |\\bimport |#include|console\\.log|=>|\\bnpm\\b|\\bpip\\b|\\bgit\\b|stack ?trace|traceback|exception"),
	r(Code, 3, "programming", `(?:^|\P{L})کد(?:ها|هام|ش|م|ت|ی)?(?:$|\P{L})|برنامهنویسی|برنامه بنویس|اسکریپت|تابع|فانکشن|متغیر|دیباگ|باگ|ارور|خطای|کامپایل|ریفکتور|پایتون|جاوااسکریپت|تایپ ?اسکریپت|گولنگ|\bcode\b|coding|program|script|function|debug|\bbug|refactor|compile|python|javascript|typescript|golang|\brust\b|\bjava\b|kotlin|swift|c\+\+|\bphp\b|\bsql\b|regex|\bapi\b|endpoint|backend|frontend|react|django|flask|laravel|\bunit tests?\b`),
	r(Project, 4, "app", `اپلیکیشن|اپ اندروید|اپ کامل|سایت کامل|وبسایت کامل|فروشگاه اینترنتی|ربات تلگرام|پروژه کامل|سیستم کامل|پنل مدیریت|\bfull[- ]stack\b|\bapp\b.*\b(build|make|create)|build (an?|the|me) (app|website|site|bot|system)|from scratch|end[- ]to[- ]end|production[- ]ready|deploy|dockerfile|kubernetes|ci/cd|microservice`),
	r(Project, 3, "servers", `سرور|دیپلوی|داکر|انجینکس|\bssh\b|\bvps\b|nginx|docker|systemd|systemctl|firewall|ssl|domain setup|database migration`),
	r(Research, 4, "research", `تحقیق|پژوهش|بررسی جامع|بررسی کامل|مقایسه کامل|مقاله علمی|منابع معتبر|گزارش کامل|پایان ?نامه|research|literature|in[- ]depth|comprehensive|thorough|white ?paper|thesis|citations?|sources`),
	r(Research, 2, "analysis", `مقایسه|تحلیل|بررسی کن|مزایا و معایب|کدوم بهتره|compare|comparison|analy[sz]e|analysis|pros and cons|trade-?offs?|which is better`),
	r(Reasoning, 4, "math", `ریاضی|معادله|انتگرال|مشتق|اثبات کن|ماتریس|هندسه|معما|الگوریتم|بهینه ?سازی|equation|integral|derivative|\bproof\b|\bprove\b|theorem|matrix|probability|geometry|puzzle|\blogic\b|algorithm|optimi[sz]|complexity|\d+\s*[\^×*/]\s*\d+\s*[+\-=]`),
	r(Reasoning, 2, "planning", `برنامه ریزی|استراتژی|نقشه راه|تصمیم|planning|strategy|roadmap|decide|decision`),
	r(Documents, 3, "documents", `پایان ?نامه|قرارداد|صورت ?جلسه|اکسل|دیتاست|دیتابیس|گزارش مالی|\bpdf\b|\bdocx?\b|\bxlsx?\b|\bcsv\b|spreadsheet|dataset|database|contract|invoice`),
	r(Writing, 3, "writing", `بنویس|متن(?:$|\P{L}|ی|ش|و)|مقاله|ایمیل|نامه|کپشن|(?:^|\P{L})پست(?:$|\P{L}|ی|م|ت|ش|ها)|شعر|داستان|انشا|بازنویسی|ویرایش کن|خلاصه|ترجمه|write|rewrite|essay|article|email|letter|caption|post|poem|story|summari[sz]e|summary|translate|paraphrase|proofread`),
	r(Media, 4, "media", `دانلود|آهنگ|موزیک|موسیقی|ویدیو|ویدئو|کلیپ|جستجوی عکس|(?:عکس|تصویر).{0,40}(?:پیدا کن|جستجو کن)|تبدیل کن|(?:^|\P{L})ویس(?:$|\P{L})|(?:^|\P{L})صدا(?:$|\P{L})|زیرنویس|youtu|instagram|tiktok|spotify|soundcloud|download|song|music|video|mp3|mp4|image search|find (?:an? )?(?:image|picture|photo)|convert|voice|subtitle`),
	r(Quick, 3, "quick", `قیمت|دلار|یورو|طلا|سکه|بیت ?کوین|(?:^|\P{L})هوا(?:$|\P{L})|ساعت چنده|امروز چندمه|یادم بنداز|یادآوری|معنی|یعنی چی|چیه\??$|کیه\??$|کجاست|کی بود|price|weather|what time|what date|remind me|meaning of|what is|who is|where is|define`),
	r(Chat, 2, "chat", `سلام|خوبی|چطوری|مرسی|ممنون|دمت گرم|نظرت|حالت|حوصله|دلم|hello|\bhi\b|thanks|thank you|how are you|what do you think|opinion|feel`),
}

var hardRe = regexp.MustCompile(`(?i)کامل|حرفه ای|دقیق|جامع|پیچیده|پیشرفته|بهینه|معماری|استاندارد|مقیاس پذیر|بدون خطا|بی نقص|مرحله به مرحله|قدم به قدم|` +
	`complete|professional|detailed|comprehensive|complex|advanced|optimi[sz]ed|architecture|robust|scalable|production|flawless|thorough|step by step`)

var (
	listLineRe = regexp.MustCompile(`(?m)^\s*(?:[-*•]|\d+[.)]|[۰-۹]+[.)])\s+\S`)
	codeExts   = map[string]bool{".py": true, ".js": true, ".ts": true, ".go": true, ".rs": true, ".java": true, ".kt": true, ".c": true, ".cpp": true,
		".h": true, ".cs": true, ".php": true, ".rb": true, ".swift": true, ".dart": true, ".sql": true, ".sh": true, ".html": true, ".css": true,
		".jsx": true, ".tsx": true, ".vue": true, ".json": true, ".yaml": true, ".yml": true, ".toml": true, ".ipynb": true}
	docExts = map[string]bool{".pdf": true, ".docx": true, ".doc": true, ".xlsx": true, ".xls": true, ".csv": true, ".pptx": true, ".epub": true,
		".odt": true, ".sqlite": true, ".db": true}
	archiveExts = map[string]bool{".zip": true, ".rar": true, ".7z": true, ".tar": true, ".gz": true, ".tgz": true}
)

func Classify(in Input) Verdict {
	text := strings.ReplaceAll(in.Text, "‌", "")
	hint := strings.ReplaceAll(in.Hint, "‌", "")
	v := Verdict{Scores: map[Kind]int{}}
	why := map[Kind][]Signal{}
	add := func(k Kind, w int, s Signal) {
		v.Scores[k] += w
		why[k] = append(why[k], s)
	}
	for _, ru := range rules {
		if n := distinct(ru.re, text); n > 0 {
			add(ru.kind, ru.weight+min(n-1, 2), Signal{Why: ru.why})
		}
	}
	if len(v.Scores) == 0 && hint != "" {
		for _, ru := range rules {
			if ru.re.MatchString(hint) {
				add(ru.kind, (ru.weight+1)/2, Signal{Why: ru.why, Hint: true})
			}
		}
	}
	for _, f := range in.Files {
		ext := strings.ToLower(path.Ext(f))
		switch {
		case ext == ".apk" || ext == ".aab" || ext == ".apks" || ext == ".xapk":
			add(Code, 4, Signal{Why: "android_package", Arg: ext})
		case codeExts[ext]:
			add(Code, 3, Signal{Why: "code_file", Arg: ext})
		case docExts[ext]:
			add(Documents, 4, Signal{Why: "document_file", Arg: ext})
		case archiveExts[ext]:
			add(Project, 3, Signal{Why: "archive_file", Arg: ext})
		}
	}
	if in.Images > 0 {
		add(Vision, 3+in.Images, Signal{Why: "pictures", Arg: strconv.Itoa(in.Images)})
	}

	best := 0
	for i := len(Kinds) - 1; i >= 0; i-- {
		if k := Kinds[i]; v.Scores[k] > best {
			v.Kind, best = k, v.Scores[k]
		}
	}
	if v.Kind == "" {
		v.Kind = Chat
		if strings.ContainsAny(text, "?؟") {
			v.Kind = Quick
		}
		v.Signals = append(v.Signals, Signal{Why: "none"})
	} else {
		v.Signals = append(v.Signals, why[v.Kind]...)
	}

	runes := utf8.RuneCountInString(text)
	hard := func(why, arg string) {
		v.Hard = true
		v.Signals = append(v.Signals, Signal{Why: why, Arg: arg})
	}
	switch {
	case in.Deep:
		hard("deep", "")
	case runes > 1500:
		hard("long", strconv.Itoa(runes))
	case len(listLineRe.FindAllString(text, -1)) >= 4:
		hard("list", "")
	case hardRe.MatchString(text) && runes > 40 && v.Kind != Chat:
		hard("thorough", "")
	}
	return v
}

func distinct(re *regexp.Regexp, text string) int {
	seen := map[string]bool{}
	for _, m := range re.FindAllString(text, 8) {
		seen[strings.ToLower(strings.TrimFunc(m, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }))] = true
	}
	return len(seen)
}

func (v Verdict) Also() []Kind {
	var out []Kind
	for _, k := range Kinds {
		if k != v.Kind && v.Scores[k] > 0 {
			out = append(out, k)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return v.Scores[out[i]] > v.Scores[out[j]] })
	return out
}
