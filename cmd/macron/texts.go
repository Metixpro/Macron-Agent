package main

import (
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

func greeting(l lang, t time.Time) string {
	switch h := t.Hour(); {
	case h >= 5 && h < 11:
		return l.t("صبح بخیر ☀️")
	case h >= 11 && h < 16:
		return l.t("ظهرت بخیر 🌤")
	case h >= 16 && h < 20:
		return l.t("عصرت بخیر 🌇")
	default:
		return l.t("شبت بخیر 🌙")
	}
}

func esc(s string) string { return telegram.EscapeHTML(s) }

const (
	textClosed            = "فعلاً عضو جدید نمی‌پذیریم. یه کم دیگه دوباره امتحان کن 🙏"
	textBusy              = "هنوز دارم روی درخواست قبلی‌ات کار می‌کنم ⏳ یه لحظه صبر کن تا تموم بشه، یا با /cancel متوقفش کن."
	textCapacity          = "الان سرم خیلی شلوغه 😅 چند ثانیه دیگه دوباره بفرست."
	textCancelled         = "باشه، ولش کردم ✋"
	textJobRestarted      = "🔄 وسط کارت ربات ری‌استارت شد (به‌روزرسانی) و این کار نیمه‌کاره موند:\n«%s»\n\nهرچی تا اینجا ساخته بودم توی فضای کاری هست؛ «▶️ ادامه بده» رو بزن تا از همون‌جا ادامه بدم."
	textBgRestarted       = "🔄 وسط کار پس‌زمینه «%s» ربات ری‌استارت شد (به‌روزرسانی) و کار نیمه‌کاره موند.\n\nهرچی تا اینجا ساخته شده توی فضای کاری هست؛ «🔁 دوباره شروعش کن» رو بزن تا از اول با همون هدف انجامش بدم."
	textBgNoSlot          = "⚠️ کار پس‌زمینه «%s» شروع نشد: سرور الان خیلی شلوغه. چند دقیقه دیگه از /tasks دوباره امتحانش کن."
	textStoppedWork       = "⏹ متوقفش کردم. تا اینجا %s قدم انجام شده و هرچی ساختم توی فضای کاری مونده؛ «▶️ ادامه بده» رو بزن یا بگو «ادامه بده» تا از همین‌جا ادامه بدم."
	textNothingToCancel   = "الان کاری در جریان نیست که بخوام متوقفش کنم."
	textNoFiles           = "هنوز فایلی برات نساختم. هر وقت چیزی ساختم، اینجا پیداش می‌کنی."
	textUnknownCommand    = "این دستور رو نمی‌شناسم 🤔 /help رو ببین."
	textCustomNone        = "🔌 روی حالت Custom هستی ولی هیچ پروایدر روشنی نداری. از /providers یکی اضافه یا روشن کن، یا با /plan برگرد روی Free یا VIP."
	textCustomFailed      = "🔌 پروایدر خودت جواب نداد: %s\n\nاز /providers تستش کن یا یکی دیگه اضافه کن (با چندتا، خودکار سوییچ می‌کنم). با /plan هم می‌تونی برگردی روی Free یا VIP."
	textProviderMissing   = "هنوز راه‌اندازی‌ام کامل نشده و نمی‌تونم جواب بدم. یه کم دیگه دوباره امتحان کن 🙏"
	textMemoryReset       = "گفتگومون از نو شروع شد ✨ (چیزهایی که درباره‌ات یادمه سر جاشونه؛ /memory) چی تو ذهنته؟"
	textMemoryCompacted   = "<i>🧠 حرف‌هامون زیاد شده بود؛ خلاصه‌شون رو نگه داشتم که چیزی یادم نره.</i>"
	textFloodWait         = "یه کم آروم‌تر 😄 حدود %d ثانیه دیگه دوباره بفرست."
	textError             = "یه مشکلی پیش اومد و نتونستم جواب بدم 😕 دوباره بفرست؛ بابت این یکی توکنی ازت کم نشد."
	textPolicyViolation   = "این درخواست با قوانین ایمنی سرویس‌های هوش مصنوعی سازگار نیست و نتونستم انجامش بدم 🙏 اگه منظورت چیز دیگه‌ای بود، یه جور دیگه بپرس. بابت این یکی توکنی ازت کم نشد."
	textTimeout           = "این کار بیشتر از حد معمول طول کشید و نیمه‌کاره موند ⏱ اگه بشه ساده‌ترش کن یا تکه‌تکه بپرس."
	textModelDown         = "الان به مغزم دسترسی ندارم 😵‍💫 چند دقیقه دیگه دوباره امتحان کن؛ توکنی ازت کم نشد."
	textFileTooBig        = "فایلت خیلی بزرگه 🙈 تا %s رو می‌تونم بگیرم."
	textPicturesHeld      = "🖼 %s عکس دیگه هم توی /files هست؛ هر درخواست تا همین تعداد عکس می‌فرسته."
	textWorkspaceFull     = "فضای فایل‌هات پره 📁 این فایل جا نمی‌شه؛ با /files چندتا فایل قدیمی رو پاک کن و دوباره بفرستش."
	textJoinThanks        = "مرسی که عضو شدی 🌱 حالا هرچی می‌خوای بپرس."
	textJoinThanksWorking = "مرسی که عضو شدی 🌱 رفتم سراغ درخواستت…"
	textJoinStill         = "هنوز عضو همه کانال‌ها نشدی 🙂"
	textNoSchedules       = "هیچ یادآور یا کار زمان‌بندی‌شده‌ای نداری.\nمثلاً بگو: «فردا ساعت ۱۱ یادم بنداز به مهدی زنگ بزنم» یا «هر روز ساعت ۱۰ قیمت تتر و طلا رو بفرست»."
	textGuestFiles        = "📁 فایل‌هایی که ساختم توی پیوی منتظرتن: @%s رو باز کن و /files رو بزن."
	textMoreFiles         = "📁 %d تا فایل دیگه هم ساختم؛ با /files همه رو ببین و هر کدوم رو خواستی بگیر."
	textSendFailed        = "نشد فایل «%s» رو بفرستم 😕"
	textFileOverLimit     = "فایل «%s» حدود %d مگه و از سقف %s تلگرام بزرگ‌تره 🙈"
	textGuestMore         = "ادامه‌اش طولانی بود؛ توی پیوی @%s کامل‌تر جواب می‌دم 🙂"
	textGuestNoAnswer     = "🤔 این بار جوابی آماده نشد؛ یه بار دیگه بپرس."
	textGuestJoinAgain    = "بعد از عضویت دوباره صدام کن."
)

func statusFor(l lang, tool string) string {
	text := toolStatus(tool)
	if text == "" {
		return ""
	}
	return statusIcon(tool) + " " + l.t(text)
}

func thinkingStatus(l lang) string { return "💭 " + l.t(thinkingText) }

func statusIcon(tool string) string {
	switch tool {
	case "web_search":
		return "🔎"
	case "web_fetch":
		return "📖"
	case "deep_research":
		return "🧠"
	case "search_music":
		return "🎧"
	case "download_file", "download_media_ytdlp":
		return "📥"
	case "generate_image":
		return "🎨"
	case "search_image", toolProfilePhoto:
		return "🖼"
	case "text_to_speech":
		return "🎙"
	case "generate_chart":
		return "📊"
	case "generate_qr":
		return "🔳"
	case "crypto_currency_price":
		return "💹"
	case tools.ToolMarketPrice:
		return "💱"
	case "whois_lookup", "browser", tools.ToolBrowserAgent:
		return "🌐"
	case "github_inspect":
		return "🐙"
	case "media_convert":
		return "🔄"
	case "split_video":
		return "✂️"
	case "create_motion_graphic":
		return "🎬"
	case "generate_music_synth":
		return "🎹"
	case tools.ToolSong:
		return "🎤"
	case "document_reader":
		return "📚"
	case tools.ToolQueryData, tools.ToolCreateSpreadsheet:
		return "📈"
	case "pdf_tool":
		return "📄"
	case "bash_execute":
		return "⚙️"
	case "run_code":
		return "💻"
	case "install_package":
		return "📦"
	case tools.ToolProjectTree, tools.ToolCodeOutline:
		return "🗂"
	case tools.ToolSearchCode, tools.ToolFindFiles:
		return "🔍"
	case tools.ToolEditFile, tools.ToolApplyPatch:
		return "✏️"
	case tools.ToolShellJobs:
		return "🛰"
	case tools.ToolExtract, tools.ToolImportRepo:
		return "📂"
	case tools.ToolPackFiles:
		return "🗜"
	case toolViewImage:
		return "👀"
	case toolLookup:
		return "🪪"
	case toolSendFile:
		return "📤"
	case toolAskUser:
		return "🙋"
	case toolSchedule:
		return "⏰"
	case toolListSchedules, toolCancelSchedule:
		return "🗓"
	case "write_file":
		return "📝"
	case "read_file", "list_files":
		return "📁"
	}
	return "🛠"
}

func toolStatus(tool string) string {
	switch tool {
	case "web_search":
		return "دارم توی اینترنت می‌گردم…"
	case "web_fetch":
		return "دارم صفحه رو می‌خونم…"
	case "deep_research":
		return "دارم یه تحقیق حسابی می‌کنم؛ چند دقیقه‌ای طول می‌کشه…"
	case "search_music":
		return "دارم آهنگ رو پیدا می‌کنم…"
	case "download_file", "download_media_ytdlp":
		return "دارم فایل رو دانلود می‌کنم…"
	case "generate_image":
		return "دارم عکس رو می‌سازم…"
	case tools.ToolEditImage:
		return "دارم عکس رو ویرایش می‌کنم؛ یکی دو دقیقه طول می‌کشه…"
	case "search_image":
		return "دارم یه عکس خوب پیدا می‌کنم…"
	case "text_to_speech":
		return "دارم صداش رو آماده می‌کنم…"
	case "generate_chart":
		return "دارم نمودار رو می‌کشم…"
	case "generate_qr":
		return "دارم QR رو می‌سازم…"
	case "crypto_currency_price", tools.ToolMarketPrice:
		return "دارم قیمت‌ها رو چک می‌کنم…"
	case "whois_lookup":
		return "دارم اطلاعات دامنه رو پیدا می‌کنم…"
	case "github_inspect":
		return "دارم ریپو رو نگاه می‌کنم…"
	case "media_convert":
		return "دارم فایل رو تبدیل می‌کنم…"
	case "split_video":
		return "دارم فایل رو بدون افت کیفیت چندتکه می‌کنم…"
	case "browser":
		return "دارم توی مرورگر کار می‌کنم…"
	case tools.ToolBrowserAgent:
		return "ایجنت مرورگر داره قدم‌به‌قدم توی سایت کار می‌کنه…"
	case "create_motion_graphic":
		return "دارم موشن‌گرافیک رو فریم‌به‌فریم رندر می‌کنم…"
	case "generate_music_synth":
		return "دارم آهنگ رو می‌سازم…"
	case tools.ToolSong:
		return "دارم آهنگ رو با هوش مصنوعی می‌سازم؛ حدود دو دقیقه طول می‌کشه…"
	case "document_reader":
		return "دارم لای صفحه‌های سند رو می‌گردم…"
	case tools.ToolQueryData:
		return "دارم داده‌ها رو تحلیل می‌کنم…"
	case tools.ToolCreateSpreadsheet:
		return "دارم فایل اکسل رو می‌سازم…"
	case "pdf_tool":
		return "دارم روی PDF کار می‌کنم…"
	case "bash_execute":
		return "دارم حسابش می‌کنم…"
	case "run_code":
		return "دارم کد رو اجرا می‌کنم…"
	case "install_package":
		return "دارم کتابخونه‌ها رو نصب می‌کنم…"
	case tools.ToolProjectTree, tools.ToolCodeOutline:
		return "دارم ساختار پروژه رو بررسی می‌کنم…"
	case tools.ToolSearchCode, tools.ToolFindFiles:
		return "دارم توی کدها می‌گردم…"
	case tools.ToolEditFile, tools.ToolApplyPatch:
		return "دارم کد رو ویرایش می‌کنم…"
	case tools.ToolShellJobs:
		return "دارم کارهای پس‌زمینه رو بررسی می‌کنم…"
	case tools.ToolExtract, tools.ToolImportRepo:
		return "دارم پروژه رو دریافت می‌کنم…"
	case tools.ToolPackFiles:
		return "دارم فایل‌ها رو zip می‌کنم…"
	case toolViewImage:
		return "دارم نتیجه رو نگاه می‌کنم…"
	case toolSendFile:
		return "دارم فایل رو آماده ارسال می‌کنم…"
	case toolAskUser:
		return "یه سؤال ازت دارم…"
	case toolSchedule:
		return "دارم زمان‌بندی‌اش رو تنظیم می‌کنم…"
	case toolListSchedules, toolCancelSchedule:
		return "دارم یادآورهات رو نگاه می‌کنم…"
	case "write_file":
		return "دارم فایل رو آماده می‌کنم…"
	case "read_file", "list_files":
		return "دارم فایل‌ها رو نگاه می‌کنم…"
	case toolProfilePhoto:
		return "دارم عکس پروفایلت رو نگاه می‌کنم…"
	case toolReact:
		return ""
	case tools.ToolListSkills, tools.ToolReadSkill:
		return "دارم دستورالعمل این کار رو مرور می‌کنم…"
	case tools.ToolSaveSkill:
		return "دارم این روش رو برای دفعه‌های بعد یاد می‌گیرم…"
	case agent.ToolDelegate:
		return "دارم یه بخش از کار رو به یه دستیار کمکی می‌سپارم…"
	}
	return "دارم روش کار می‌کنم…"
}

func actionFor(tool string) string {
	switch tool {
	case "generate_image", tools.ToolEditImage, "search_image", "generate_chart", "generate_qr", toolProfilePhoto:
		return "upload_photo"
	case "text_to_speech":
		return "record_voice"
	case "download_media_ytdlp", "media_convert", "split_video", "create_motion_graphic":
		return "upload_video"
	case "generate_music_synth", tools.ToolSong:
		return "upload_voice"
	case "download_file", "pdf_tool", "write_file", toolSendFile:
		return "upload_document"
	}
	return "typing"
}

const thinkingText = "دارم فکر می‌کنم…"
