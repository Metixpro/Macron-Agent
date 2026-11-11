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
		[]string{"search_image", "view_image", "media_convert"}},
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
		[]string{tools.ToolMotion}},
	{regexp.MustCompile(`(?i)@\w{4,}|t\.me/|آیدی|یوزرنیم|اکانت|کانال|گروه|ممبر|عضو|\bban\b|بن کن|سکوت|mute|username|account|channel|group|member`),
		[]string{"telegram_lookup", "chat_info", "look_at_profile_photo"}},
	{regexp.MustCompile(`(?i)تاس|بازی|نظرسنجی|کوییز|مسابقه|لوکیشن|نقشه|موقعیت|dice|poll|quiz|game|location|\bmap\b|confetti|افکت`),
		[]string{"roll_dice", "create_poll", "send_location", "message_effect"}},
	{regexp.MustCompile(`(?i)ویجت|ویدجت|widget|داشبورد|dashboard|مینی ?اپ|mini ?app|پنل تعاملی|ماشین ?حساب|calculator|interactive`),
		[]string{"send_widget", "update_widget", "read_skill"}},
	{regexp.MustCompile(`(?i)دامنه|domain|whois|github\.com`),
		[]string{"whois_lookup", "github_inspect"}},
	{regexp.MustCompile(`(?i)اسکیل|skill|مهارت`),
		[]string{tools.ToolListSkills, tools.ToolReadSkill, tools.ToolSaveSkill}},
	{regexp.MustCompile(`(?i)حافظه|یادت|فراموش|remember|forget|memory`),
		[]string{"remember", "forget"}},
	{regexp.MustCompile(`(?i)موازی|همزمان|هم‌زمان|زیر ?ایجنت|parallel|sub-?agent|delegate|چند تا کار`),
		[]string{ToolDelegate}},
	{regexp.MustCompile(`(?i)درایو|drive|google\s?drive|گوگل\s?درایو|s3|minio|webdav|nextcloud|ابری|cloud\s?storage`),
		[]string{"cloud_storage"}},
	{regexp.MustCompile(`(?i)بورس|سهام|کدال|tsetmc|codal|شاخص|نماد|فولاد|فملی|شستا|خودرو|شپنا`),
		[]string{"tsetmc_stock"}},
	{regexp.MustCompile(`(?i)هشدار\s?قیمت|price\s?alert|آلارم\s?قیمت`),
		[]string{"price_alert", tools.ToolMarketPrice, "crypto_currency_price"}},
	{regexp.MustCompile(`(?i)پایش|مانیتور|page\s?watch|تغییرات\s?سایت`),
		[]string{"page_watch", "web_fetch"}},
	{regexp.MustCompile(`(?i)مقاله\s?علمی|arxiv|scholar|pubmed|academic|علمی|پژوهشی`),
		[]string{"academic_search", "deep_research"}},
	{regexp.MustCompile(`(?i)راستی\s?آزمایی|fact\s?check|فکت\s?چک|صحت\s?سنجی|راست\s?آزمایی|شایعه|صحت`),
		[]string{"fact_check", "web_search"}},
	{regexp.MustCompile(`(?i)docx?|word|ورد|pptx?|powerpoint|پاورپوینت|xlsx?|اکسل|قالب|فرم`),
		[]string{tools.ToolOfficeDoc, "pdf_tool", tools.ToolDocumentReader, tools.ToolCreateSpreadsheet}},
	{regexp.MustCompile(`(?i)دیف|مقایسه\s?فایل|تفاوت\s?دو\s?فایل|document\s?diff|diff`),
		[]string{"document_diff"}},
	{regexp.MustCompile(`(?i)ocr|متن\s?از\s?عکس|تبدیل\s?عکس\s?به\s?متن|استخراج\s?متن|نویسه‌خوان`),
		[]string{"ocr_image", "view_image"}},
	{regexp.MustCompile(`(?i)ترجمه|ترجمه\s?فایل|ترجمه\s?سند|translate`),
		[]string{"translate_document"}},
	{regexp.MustCompile(`(?i)دیاگرام|فلوچارت|mermaid|graphviz|رسم\s?نمودار|معماری`),
		[]string{"generate_diagram"}},
	{regexp.MustCompile(`(?i)عکس\s?بساز|تولید\s?تصویر|generate\s?image|dall-?e|تصویر\s?هوش\s?مصنوعی`),
		[]string{"generate_image", "search_image"}},
	{regexp.MustCompile(`(?i)کتاب|نگارش\s?کتاب|فصل\s?کتاب|book\s?writer|رمان|دست‌نوشته`),
		[]string{"book_writer"}},
	{regexp.MustCompile(`(?i)صورتجلسه|صورت\s?جلسه|خلاصه\s?پادکست|خلاصه\s?ویدیو|meeting\s?minutes`),
		[]string{"media_summary"}},
	{regexp.MustCompile(`(?i)کوئری|query|select|postgres|mysql|sqlite|پایگاه\s?داده|دیتابیس|sql`),
		[]string{"database_query", tools.ToolQueryData}},
	{regexp.MustCompile(`(?i)داشبورد|dashboard|html\s?dashboard`),
		[]string{"create_dashboard"}},
	{regexp.MustCompile(`(?i)openapi|swagger|فراخوانی\s?api|api\s?call`),
		[]string{"openapi_call"}},
	{regexp.MustCompile(`(?i)آمار\s?توصیفی|رگرسیون|پیش\s?بینی\s?داده|forecast|statistics|آماری`),
		[]string{"statistics_forecast"}},
	{regexp.MustCompile(`(?i)تعریف\s?تابع|کجا\s?تعریف\s?شده|symbol|definition|lsp|سمبل`),
		[]string{"code_intelligence", tools.ToolSearchCode, tools.ToolCodeOutline}},
	{regexp.MustCompile(`(?i)پول\s?ریکوئست|pull\s?request|pr\s?بساز|ایشو|issue`),
		[]string{"github_workflow", "github_inspect"}},
	{regexp.MustCompile(`(?i)پیش\s?نمایش\s?زنده|live\s?preview|لوکال\s?هاست`),
		[]string{"live_preview"}},
	{regexp.MustCompile(`(?i)ایمیل|email|mail|جیمیل|smtp`),
		[]string{"email_client"}},
	{regexp.MustCompile(`(?i)تقویم|رویداد|جلسه|calendar|ics|icalendar`),
		[]string{"calendar_client", "schedule"}},
	{regexp.MustCompile(`(?i)بله|ایتا|روبیکا|bale|eitaa|rubika`),
		[]string{"iran_messenger"}},
	{regexp.MustCompile(`(?i)دیسکورد|اسلک|واتساپ|discord|slack|whatsapp`),
		[]string{"external_messenger"}},
	{regexp.MustCompile(`(?i)ترلو|نوشن|تودوایست|trello|notion|todoist`),
		[]string{"task_board"}},
	{regexp.MustCompile(`(?i)دیپلوی|استقرار|deploy|سرویس|rollback`),
			[]string{"deploy_service"}},
	{regexp.MustCompile(`(?i)گزارش\s?صبحگاهی|صبحگاهی|morning\s?briefing`),
		[]string{"generate_briefing", tools.ToolMarketPrice, "crypto_currency_price", "tsetmc_stock"}},
	{regexp.MustCompile(`(?i)بکاپ\s?رمزنگاری|پشتیبان\s?رمز|backup|رمزنگاری`),
		[]string{"encrypted_cloud_backup"}},
	{regexp.MustCompile(`(?i)اینباکس|inbox|پوشه\s?ورودی`),
		[]string{"process_inbox"}},
	{regexp.MustCompile(`(?i)جستجو\s?در\s?فایل|rag|پیدا\s?کردن\s?در\s?اسناد`),
		[]string{"rag_search", tools.ToolDocumentReader}},
	{regexp.MustCompile(`(?i)کرنل|پایتون\s?کرنل|kernel|متغیرها`),
		[]string{"python_kernel", tools.ToolRunCode}},
}

var toolGroupInfo = []struct{ key, about string }{
	{"media", "download, convert, split videos, music or audio"},
	{"images", "find or inspect pictures"},
	{"documents", "read or make PDFs, Word, Excel, CSV or slides, or pack and unpack archives"},
	{"code", "write, run, fix or test code or a software project"},
	{"prices", "currency, gold or crypto prices"},
	{"reminders", "a reminder or a scheduled job"},
	{"voice", "speak text aloud or convert audio files"},
	{"charts", "a chart, a graph or a QR code"},
	{"research", "in-depth research with sources and a report"},
	{"browser", "open a website in a real browser: log in, click, fill forms, take screenshots"},
	{"motion", "an animation, a motion graphic or an intro video"},
	{"telegram", "look up Telegram users, channels or groups"},
	{"games", "dice, polls, quizzes, a location on a map, or a message effect"},
	{"widgets", "an interactive widget inside the chat: a dashboard, form, calculator, game or live panel"},
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
