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
	"use " + ToolSearch + " when available to load just the tools you need; otherwise call " + ToolMoreTools + "."

var focusCore = []string{"web_search", "web_fetch", tools.ToolRunCode, "bash_execute", "read_file", "write_file", "list_files",
	"send_file", "ask_user", "react_to_message", "remember", ToolPlan, ToolSearch}

type focusGroup struct {
	key, about string
	re         *regexp.Regexp
	tools      []string
}

var focusGroups = []focusGroup{
	{"media", "download, convert, split videos, music or audio", regexp.MustCompile(`(?i)youtu|instagram|tiktok|aparat|soundcloud|spotify|twitter|x\.com/|t\.me/|ویدیو|ویدئو|فیلم|کلیپ|آهنگ|اهنگ|موزیک|موسیقی|دانلود|music|song|video|download|mp3|mp4|podcast|پادکست|a video|an audio`),
		[]string{tools.ToolDownloadMedia, "search_music", "download_file", "media_convert", tools.ToolSplitVideo}},
	{"images", "find or inspect pictures", regexp.MustCompile(`(?i)عکس|تصویر|نقاشی|لوگو|پوستر|بنر|استیکر|image|photo|picture|logo|draw|poster|wallpaper|sticker|\.(?:png|jpe?g|webp)\b`),
		[]string{"search_image", "view_image", "media_convert"}},
	{"documents", "read or make PDFs, Word, Excel, CSV or slides, or pack and unpack archives", regexp.MustCompile(`(?i)pdf|\bword\b|docx?|excel|xlsx|csv|pptx?|پاورپوینت|اکسل|ورد|سند|جدول|کتاب|\bbook\b|sheet|spreadsheet|فایل|\bfile|zip|rar|7z|آرشیو|فشرده|database|sqlite|دیتابیس`),
		[]string{"pdf_tool", tools.ToolDocumentReader, tools.ToolQueryData, tools.ToolCreateSpreadsheet, tools.ToolExtract, tools.ToolPackFiles}},
	{"code", "write, run, fix or test code or a software project", regexp.MustCompile(`(?i)کد|برنامه|اسکریپت|پایتون|python|javascript|typescript|node|golang|rust|php|java|kotlin|c\+\+|html|css|\bapi\b|bug|باگ|ارور|error|github|گیت|repo|پروژه|project|اپ|\bapp\b|android|flutter|build|بیلد|compile|install|نصب|پکیج|package|library|کتابخونه`),
		[]string{tools.ToolRunCode, "bash_execute", tools.ToolEditFile, tools.ToolApplyPatch, tools.ToolCodeOutline, tools.ToolSearchCode, tools.ToolFindFiles,
			tools.ToolProjectTree, tools.ToolImportRepo, "github_inspect", tools.ToolShellJobs, "background_task"}},
	{"prices", "currency, gold or crypto prices", regexp.MustCompile(`(?i)قیمت|دلار|یورو|درهم|طلا|سکه|مثقال|ارز|بیت ?کوین|تتر|کریپتو|price|dollar|euro|gold|bitcoin|btc|\beth\b|crypto|usdt|toman|تومن|اشتراک|vip|پلن|استارز|stars`),
		[]string{tools.ToolMarketPrice, "crypto_currency_price"}},
	{"reminders", "a reminder or a scheduled job", regexp.MustCompile(`(?i)یادآور|یادم بنداز|یادم بیار|هر روز|هر هفته|هر ساعت|ساعت ?\d|فردا|زمان ?بندی|remind|schedule|every (?:day|week|hour|morning)|tomorrow|alarm|cron`),
		[]string{"schedule", "list_schedules", "cancel_schedule"}},
	{"voice", "speak text aloud or convert audio files", regexp.MustCompile(`(?i)صدا|ویس|voice|audio|speech|بخون|بخوان|read (?:it )?aloud|تلفظ|recording|ضبط`),
		[]string{"text_to_speech", "media_convert"}},
	{"charts", "a chart, a graph or a QR code", regexp.MustCompile(`(?i)نمودار|چارت|chart|graph|plot|\bqr\b|کیو ?آر|آمار|statistic`),
		[]string{"generate_chart", "generate_qr", tools.ToolQueryData}},
	{"research", "in-depth research with sources and a report", regexp.MustCompile(`(?i)تحقیق|پژوهش|مقاله|research|paper|گزارش کامل|بررسی جامع|in-depth|literature`),
		[]string{"deep_research"}},
	{"browser", "open a website in a real browser: log in, click, fill forms, take screenshots", regexp.MustCompile(`(?i)سایت|وبسایت|website|browser|مرورگر|لاگین|login|sign in|فرم|\bform\b|اسکرین ?شات|screenshot|کلیک|click|https?://`),
		[]string{tools.ToolBrowser, tools.ToolBrowserAgent, "web_fetch"}},
	{"motion", "an animation, a motion graphic or an intro video", regexp.MustCompile(`(?i)انیمیشن|موشن|اینترو|تیتراژ|animation|motion|intro|animated`),
		[]string{tools.ToolMotion}},
	{"telegram", "look up Telegram users, channels or groups", regexp.MustCompile(`(?i)@\w{4,}|t\.me/|آیدی|یوزرنیم|اکانت|کانال|گروه|ممبر|عضو|\bban\b|بن کن|سکوت|mute|username|account|channel|group|member`),
		[]string{"telegram_lookup", "chat_info", "look_at_profile_photo"}},
	{"games", "dice, polls, quizzes, a location on a map, or a message effect", regexp.MustCompile(`(?i)تاس|بازی|نظرسنجی|کوییز|مسابقه|لوکیشن|نقشه|موقعیت|dice|poll|quiz|game|location|\bmap\b|confetti|افکت`),
		[]string{"roll_dice", "create_poll", "send_location", "message_effect"}},
	{"widgets", "an interactive widget inside the chat: a dashboard, form, calculator, game or live panel", regexp.MustCompile(`(?i)ویجت|ویدجت|widget|داشبورد|dashboard|مینی ?اپ|mini ?app|پنل تعاملی|ماشین ?حساب|calculator|interactive`),
		[]string{"send_widget", "update_widget", "read_skill"}},
	{"domains", "a domain's whois or a public GitHub repository", regexp.MustCompile(`(?i)دامنه|domain|whois|github\.com`),
		[]string{"whois_lookup", "github_inspect"}},
	{"skills", "the bot's saved skills (procedures)", regexp.MustCompile(`(?i)اسکیل|skill|مهارت`),
		[]string{tools.ToolListSkills, tools.ToolReadSkill, tools.ToolSaveSkill}},
	{"memory", "remember or forget a fact about the user", regexp.MustCompile(`(?i)حافظه|یادت|فراموش|remember|forget|memory`),
		[]string{"remember", "forget"}},
	{"parallel", "split a big job among parallel sub-agents", regexp.MustCompile(`(?i)موازی|همزمان|هم‌زمان|زیر ?ایجنت|parallel|sub-?agent|delegate|چند تا کار`),
		[]string{ToolDelegate}},
	{"cloud_storage", "S3, MinIO, R2, WebDAV or Nextcloud file storage", regexp.MustCompile(`(?i)\bs3\b|minio|\br2\b|webdav|nextcloud|owncloud|فضای ابری|cloud storage|\bbucket\b|باکت`),
		[]string{tools.ToolCloudStorage}},
	{"stocks", "Tehran stock exchange prices, shares and indices", regexp.MustCompile(`(?i)بورس|سهام|کدال|tsetmc|codal|شاخص کل|فرابورس|tehran stock|\bstocks?\b`),
		[]string{tools.ToolTSETMCStock}},
	{"price_alerts", "price alerts and notifications", regexp.MustCompile(`(?i)هشدار قیمت|آلارم قیمت|خبرم کن وقتی|price alert|alert me when|notify me when`),
		[]string{tools.ToolPriceAlert, tools.ToolMarketPrice, "crypto_currency_price", "schedule"}},
	{"page_watch", "monitor a page or website for changes", regexp.MustCompile(`(?i)تغییرات (?:سایت|صفحه)|(?:پایش|مانیتور) (?:سایت|صفحه)|page ?watch|monitor (?:a |the |this )?(?:page|site|website)|watch (?:a |the |this )?(?:page|site)`),
		[]string{tools.ToolPageWatch, "schedule"}},
	{"academic", "academic papers, journals and citations", regexp.MustCompile(`(?i)arxiv|scholar|pubmed|academic|مقالات? (?:علمی|پژوهشی|isi)|ژورنال|journal|peer.reviewed|citations?`),
		[]string{tools.ToolAcademicSearch}},
	{"fact_check", "verify claims with sources", regexp.MustCompile(`(?i)راستی ?آزمایی|fact.?check|فکت ?چک|صحت ?سنجی|شایعه|is it true that|true or false`),
		[]string{tools.ToolFactCheck}},
	{"office", "create Word documents or PowerPoint slides", regexp.MustCompile(`(?i)\.docx|\.pptx|\bword\b|(?:^|\s)ورد(?:\s|$)|powerpoint|پاورپوینت|اسلاید|\bslides?\b|presentation`),
		[]string{tools.ToolOfficeDoc, tools.ToolDocumentReader}},
	{"document_diff", "compare files, documents or versions", regexp.MustCompile(`(?i)(?:مقایسه|تفاوت|فرق) (?:دو |این دو )?(?:فایل|سند|نسخه)|(?:فایل|سند|نسخه).{0,20}(?:مقایسه|تفاوت|فرق)|compare (?:the |two |these )?(?:files|documents|versions)|\bdiff\b`),
		[]string{tools.ToolDocumentDiff}},
	{"ocr", "extract text from images or scans", regexp.MustCompile(`(?i)\bocr\b|متن (?:این |داخل |توی |روی )?(?:عکس|تصویر|اسکن)|(?:عکس|تصویر) به متن|نویسه ?خوان|text (?:from|in|on) (?:the |this |an? )?(?:image|photo|picture|scan)`),
		[]string{tools.ToolOCR, "view_image"}},
	{"translation", "translate documents, books or subtitles", regexp.MustCompile(`(?i)ترجمه.{0,12}(?:فایل|سند|کتاب|مقاله|زیرنویس)|(?:فایل|سند|کتاب|مقاله|زیرنویس).{0,20}ترجمه|translate (?:this |the |a |my )?(?:file|document|subtitles?|book)|زیرنویس|\.srt\b`),
		[]string{tools.ToolTranslateDoc}},
	{"diagrams", "diagrams, flowcharts, Mermaid, Graphviz or UML", regexp.MustCompile(`(?i)دیاگرام|فلوچارت|flow ?chart|mermaid|graphviz|\bdiagram|\buml\b`),
		[]string{tools.ToolGenerateDiagram, "view_image"}},
	{"image_generation", "generate images, posters or logos", regexp.MustCompile(`(?i)(?:عکس|تصویر|پوستر|لوگو) (?:بساز|طراحی کن|درست کن|تولید کن)|تولید تصویر|(?:generate|create|make) (?:an? |me an? )?(?:image|picture|poster|logo)|dall.?e|image generation`),
		[]string{tools.ToolGenerateImage, "view_image"}},
	{"media_summary", "summarize recordings, transcripts or meetings", regexp.MustCompile(`(?i)صورت ?جلسه|خلاصه.{0,6}(?:پادکست|ویدیو|ویدئو|جلسه|ترنسکریپت)|meeting minutes|transcript|ترنسکریپت`),
		[]string{tools.ToolMediaSummary}},
	{"databases", "query PostgreSQL, MySQL, SQLite or other databases", regexp.MustCompile(`(?i)postgres|mysql|mariadb|پایگاه ?داده|دیتابیس|database|\bsql\b|کوئری`),
		[]string{tools.ToolDatabaseQuery, tools.ToolQueryData}},
	{"dashboards", "create dashboards from data", regexp.MustCompile(`(?i)داشبورد|dashboard`),
		[]string{tools.ToolCreateDashboard}},
	{"openapi", "call an API described by OpenAPI or Swagger", regexp.MustCompile(`(?i)openapi|swagger`),
		[]string{tools.ToolOpenAPICall}},
	{"statistics", "statistics, regression or forecasting", regexp.MustCompile(`(?i)آمار توصیفی|رگرسیون|regression|forecast|پیش ?بینی (?:روند|فروش|داده)|انحراف معیار|standard deviation|\bmedian\b|میانه`),
		[]string{tools.ToolStatsForecast}},
	{"github", "GitHub issues, pull requests or workflows", regexp.MustCompile(`(?i)pull ?request|\bpr\b|پول ?ریکوئست|ایشو|github issue`),
		[]string{tools.ToolGitHubWorkflow, "github_inspect"}},
	{"preview", "preview a local website", regexp.MustCompile(`(?i)پیش ?نمایش|\bpreview\b|localhost|لوکال ?هاست|static site|سایت استاتیک`),
		[]string{tools.ToolLivePreview}},
	{"email", "send email using SMTP, including Gmail SMTP", regexp.MustCompile(`(?i)ایمیل|e-?mail|\bsmtp\b|جیمیل|gmail`),
		[]string{tools.ToolEmailClient}},
	{"calendar", "create calendar events as iCalendar files", regexp.MustCompile(`(?i)تقویم|calendar|\.ics\b|icalendar`),
		[]string{tools.ToolCalendarClient}},
	{"iran_messengers", "send messages to Bale, Eitaa or Rubika", regexp.MustCompile(`(?i)\bbale\b|eitaa|ایتا(?:\s|$)|روبیکا|rubika|(?:ربات|کانال|گروه|پیام ?رسان) بله|(?:به|تو|در|توی) بله `),
		[]string{tools.ToolIranMessenger}},
	{"external_messengers", "send messages to Slack, Discord or WhatsApp", regexp.MustCompile(`(?i)discord|دیسکورد|slack|اسلک|whatsapp|واتس ?اپ`),
		[]string{tools.ToolExternalMessenger}},
	{"task_boards", "Notion, Trello or Todoist tasks", regexp.MustCompile(`(?i)trello|ترلو|notion|نوشن|todoist|تودوایست`),
		[]string{tools.ToolTaskBoard}},
	{"deployment", "deploy services or roll back deployments", regexp.MustCompile(`(?i)دیپلوی|\bdeploy|rollback|رول ?بک|استقرار`),
		[]string{tools.ToolDeployService}},
	{"briefing", "generate a morning or daily briefing", regexp.MustCompile(`(?i)گزارش صبحگاهی|صبحگاهی|morning briefing|daily briefing|بریفینگ`),
		[]string{tools.ToolGenerateBriefing}},
	{"rag", "search saved documents, notes or files", regexp.MustCompile(`(?i)\brag\b|(?:اسناد|مستندات|یادداشت ?ها|فایل ?ها)(?:م|ی)? (?:بگرد|جستجو)|search (?:my|the|all|these) (?:docs|documents|files|notes)`),
		[]string{tools.ToolRAGSearch}},
	{"crawl", "crawl a site or documentation", regexp.MustCompile(`(?i)\bcrawl|کراول|(?:کل|همه) (?:سایت|مستندات|صفحات)|whole (?:site|docs)|all (?:the )?pages of`),
		[]string{tools.ToolCrawlSite}},
	{"python_kernel", "run code in a persistent Python kernel", regexp.MustCompile(`(?i)python kernel|کرنل پایتون|پایتون کرنل|jupyter|ژوپیتر|notebook|نوت ?بوک`),
		[]string{tools.ToolPythonKernel}},
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
		out[i] = ToolGroup{Key: g.key, About: g.about, Tools: append([]string(nil), g.tools...)}
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
