package agent

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

const ToolMoreTools = "more_tools"

var moreToolsDef = llm.Tool{Type: "function", Function: llm.ToolDef{Name: ToolMoreTools,
	Description: "Your list holds the tools this request most likely needs. If the job needs one that is not there, call this " +
		"first, saying what you need: every tool is available from your next step.",
	Parameters: map[string]any{"type": "object", "properties": map[string]any{
		"need": map[string]any{"type": "string", "description": "What you need to do"},
	}}}}

const noticeFocused = "[tools] For this request you have the tools it most likely needs. Call them with their exact names and " +
	"parameters, one step at a time, and read each result before the next step. If the job needs a tool that is not in your list, " +
	"call " + ToolMoreTools + " first."

var focusCore = []string{"web_search", "web_fetch", tools.ToolRunCode, "bash_execute", "read_file", "write_file", "list_files",
	"send_file", "ask_user", "react_to_message", "remember", ToolPlan}

type focusGroup struct {
	re    *regexp.Regexp
	tools []string
}

var focusGroups = []focusGroup{
	{regexp.MustCompile(`(?i)youtu|instagram|tiktok|aparat|soundcloud|spotify|twitter|x\.com/|t\.me/|ویدیو|ویدئو|فیلم|کلیپ|آهنگ|اهنگ|موزیک|موسیقی|دانلود|music|song|video|download|mp3|mp4|podcast|پادکست|a video|an audio`),
		[]string{tools.ToolDownloadMedia, "search_music", "download_file", "media_convert", tools.ToolSplitVideo}},
	{regexp.MustCompile(`(?i)عکس|تصویر|نقاشی|لوگو|پوستر|بنر|استیکر|image|photo|picture|logo|draw|poster|wallpaper|sticker|\.(?:png|jpe?g|webp)\b`),
		[]string{"generate_image", "search_image", tools.ToolEditImage, "view_image", "media_convert"}},
	{regexp.MustCompile(`(?i)pdf|\bword\b|docx?|excel|xlsx|csv|pptx?|پاورپوینت|اکسل|ورد|سند|جدول|کتاب|\bbook\b|sheet|spreadsheet|فایل|\bfile|zip|rar|7z|آرشیو|فشرده|database|sqlite|دیتابیس`),
		[]string{"pdf_tool", tools.ToolDocumentReader, tools.ToolQueryData, tools.ToolCreateSpreadsheet, tools.ToolExtract, tools.ToolPackFiles}},
	{regexp.MustCompile(`(?i)کد|برنامه|اسکریپت|پایتون|python|javascript|typescript|node|golang|rust|php|java|kotlin|c\+\+|html|css|\bapi\b|bug|باگ|ارور|error|github|گیت|repo|پروژه|project|اپ|\bapp\b|android|flutter|build|بیلد|compile|install|نصب|پکیج|package|library|کتابخونه`),
		[]string{tools.ToolRunCode, "bash_execute", tools.ToolEditFile, tools.ToolApplyPatch, tools.ToolCodeOutline, tools.ToolSearchCode, tools.ToolFindFiles,
			tools.ToolProjectTree, tools.ToolImportRepo, "github_inspect", tools.ToolShellJobs, "background_task"}},
	{regexp.MustCompile(`(?i)قیمت|دلار|یورو|درهم|طلا|سکه|مثقال|ارز|بیت ?کوین|تتر|کریپتو|price|dollar|euro|gold|bitcoin|btc|\beth\b|crypto|usdt|toman|تومن|اشتراک|vip|پلن|استارز|stars`),
		[]string{tools.ToolMarketPrice, "crypto_currency_price"}},
	{regexp.MustCompile(`(?i)یادآور|یادم بنداز|یادم بیار|هر روز|هر هفته|هر ساعت|ساعت ?\d|فردا|زمان ?بندی|remind|schedule|every (?:day|week|hour|morning)|tomorrow|alarm|cron`),
		[]string{"schedule", "list_schedules", "cancel_schedule"}},
	{regexp.MustCompile(`(?i)صدا|ویس|voice|audio|speech|بخون|بخوان|read (?:it )?aloud|تلفظ|recording|ضبط`),
		[]string{"text_to_speech", "media_convert"}},
	{regexp.MustCompile(`(?i)نمودار|چارت|chart|graph|plot|\bqr\b|کیو ?آر|آمار|statistic`),
		[]string{"generate_chart", "generate_qr", tools.ToolQueryData}},
	{regexp.MustCompile(`(?i)تحقیق|پژوهش|مقاله|research|paper|گزارش کامل|بررسی جامع|in-depth|literature`),
		[]string{"deep_research"}},
	{regexp.MustCompile(`(?i)سایت|وبسایت|website|browser|مرورگر|لاگین|login|sign in|فرم|\bform\b|اسکرین ?شات|screenshot|کلیک|click|https?://`),
		[]string{tools.ToolBrowser, tools.ToolBrowserAgent, "web_fetch"}},
	{regexp.MustCompile(`(?i)انیمیشن|موشن|اینترو|تیتراژ|animation|motion|intro|animated`),
		[]string{tools.ToolMotion, tools.ToolMusic}},
	{regexp.MustCompile(`(?i)آهنگ بساز|ساخت (?:آهنگ|موزیک|موسیقی)|(?:موزیک|موسیقی|آهنگ) \S+ (?:بساز|درست کن)|بی ?کلام|instrumental|ملودی|\bبیت\b|beat|melody|jingle|compose|soundtrack|افکت صوتی|sound effect|ترانه|خواننده|با کلام|lyrics|\bsing\b|\bsong\b`),
		[]string{tools.ToolMusic, tools.ToolSong}},
	{regexp.MustCompile(`(?i)@\w{4,}|t\.me/|آیدی|یوزرنیم|اکانت|کانال|گروه|ممبر|عضو|\bban\b|بن کن|سکوت|mute|username|account|channel|group|member`),
		[]string{"telegram_lookup", "group_admin", "read_group_history", "chat_info", "look_at_profile_photo"}},
	{regexp.MustCompile(`(?i)تاس|بازی|نظرسنجی|کوییز|مسابقه|لوکیشن|نقشه|موقعیت|dice|poll|quiz|game|location|\bmap\b|confetti|افکت`),
		[]string{"roll_dice", "create_poll", "send_location", "message_effect"}},
	{regexp.MustCompile(`(?i)دامنه|domain|whois|github\.com`),
		[]string{"whois_lookup", "github_inspect"}},
	{regexp.MustCompile(`(?i)اسکیل|skill|مهارت`),
		[]string{tools.ToolListSkills, tools.ToolReadSkill, tools.ToolSaveSkill}},
	{regexp.MustCompile(`(?i)حافظه|یادت|فراموش|remember|forget|memory`),
		[]string{"remember", "forget"}},
	{regexp.MustCompile(`(?i)موازی|همزمان|هم‌زمان|زیر ?ایجنت|parallel|sub-?agent|delegate|چند تا کار`),
		[]string{ToolDelegate}},
}

var toolGroupInfo = []struct{ key, about string }{
	{"media", "download, convert, split videos, music or audio"},
	{"images", "make, find, edit or look at pictures"},
	{"documents", "read or make PDFs, Word, Excel, CSV or slides, or pack and unpack archives"},
	{"code", "write, run, fix or test code or a software project"},
	{"prices", "currency, gold or crypto prices"},
	{"reminders", "a reminder or a scheduled job"},
	{"voice", "speak text aloud or convert audio files"},
	{"charts", "a chart, a graph or a QR code"},
	{"research", "in-depth research with sources and a report"},
	{"browser", "open a website in a real browser: log in, click, fill forms, take screenshots"},
	{"motion", "an animation, a motion graphic or an intro video"},
	{"music", "compose music, a melody or a sound effect"},
	{"telegram", "Telegram users, channels and groups: look them up, moderate members, read a group's history"},
	{"games", "dice, polls, quizzes, a location on a map, or a message effect"},
	{"domains", "a domain's whois or a public GitHub repository"},
	{"skills", "the bot's saved skills (procedures)"},
	{"memory", "remember or forget a fact about the user"},
	{"parallel", "split a big job among parallel sub-agents"},
}

const MaxTools = 128

func (a *Agent) toolLimit() int {
	if n := a.toolCap.Load(); n > 0 {
		return int(n)
	}
	return MaxTools
}

var toolLimitRe = regexp.MustCompile(`(?i)(?:maximum number of items is|at most|no more than|maximum of|up to|limit of)\s*(\d{1,4})`)

func toolLimitFrom(err error) int {
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "tools") {
		return 0
	}
	m := toolLimitRe.FindStringSubmatch(err.Error())
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func capTools(tools []llm.Tool, limit int, first func(string) bool) []llm.Tool {
	if len(tools) <= limit {
		return tools
	}
	out := make([]llm.Tool, 0, limit)
	for _, ahead := range []bool{true, false} {
		for _, t := range tools {
			if len(out) == limit {
				return out
			}
			if first(t.Function.Name) == ahead {
				out = append(out, t)
			}
		}
	}
	return out
}

func FocusCore() []string { return append([]string(nil), focusCore...) }

type ToolGroup struct {
	Key, About string
	Tools      []string
}

func ToolGroups() []ToolGroup {
	out := make([]ToolGroup, len(focusGroups))
	for i, g := range focusGroups {
		out[i] = ToolGroup{Key: toolGroupInfo[i].key, About: toolGroupInfo[i].about, Tools: g.tools}
	}
	return out
}

func focusPicked(defs []llm.Tool, text string, picked []string) []llm.Tool {
	want := focusWant(defs, text)
	for _, n := range picked {
		want[n] = true
	}
	var out []llm.Tool
	for _, d := range defs {
		if want[d.Function.Name] {
			out = append(out, d)
		}
	}
	return out
}

func toolsTokens(defs []llm.Tool) int {
	n := 0
	for _, d := range defs {
		raw, _ := json.Marshal(d)
		n += len(raw)
	}
	return n / 3
}

func focusText(req Request) string {
	var sb strings.Builder
	sb.WriteString(req.Input.Content)
	for _, p := range req.Input.Parts {
		sb.WriteString("\n" + p.Text)
	}
	sb.WriteString("\n" + req.FocusHint)
	from := max(len(req.History)-4, 0)
	for _, m := range req.History[from:] {
		if m.Role == "user" {
			sb.WriteString("\n" + m.Content)
		}
		for _, c := range m.ToolCalls {
			sb.WriteString("\n" + c.Function.Name)
		}
	}
	return sb.String()
}

func focusTools(defs []llm.Tool, text string) []llm.Tool { return focusPicked(defs, text, nil) }

func focusWant(defs []llm.Tool, text string) map[string]bool {
	want := map[string]bool{}
	for _, n := range focusCore {
		want[n] = true
	}
	for _, g := range focusGroups {
		if g.re.MatchString(text) {
			for _, n := range g.tools {
				want[n] = true
			}
		}
	}
	for _, d := range defs {
		if n := d.Function.Name; len(n) > 4 && strings.Contains(text, n) {
			want[n] = true
		}
	}
	return want
}

func (a *Agent) moreTools(off *atomic.Bool) LocalTool {
	return LocalTool{Def: moreToolsDef, Handler: func(ctx context.Context, _ map[string]any) (ToolOutput, error) {
		off.Store(true)
		return ToolOutput{Text: "Every tool is available now. Carry on with the job."}, nil
	}}
}

type CallQuality struct {
	Calls   int
	Fixed   int
	Refused int
	Stalled int
}

func (q CallQuality) Problems() int { return q.Fixed + q.Refused + q.Stalled }

func (r *Result) quality(provider string, f func(*CallQuality)) {
	if r.CallQuality == nil {
		r.CallQuality = map[string]CallQuality{}
	}
	q := r.CallQuality[provider]
	f(&q)
	r.CallQuality[provider] = q
}

func (r *Result) slips() int {
	n := 0
	for _, q := range r.CallQuality {
		n += q.Problems()
	}
	return n
}
