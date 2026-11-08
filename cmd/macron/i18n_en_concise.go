package main

func init() {
	english["[!] نسخه نصب شد؛ راه‌اندازی مجدد ناموفق بود: %s\n<code>macron service restart</code>"] = "[!] Version installed; restart failed: %s\n<code>macron service restart</code>"
	english["🖥 <b>نمایش کار</b>\n\nنمایش مراحل: %s"] = "🖥 <b>Work display</b>\n\nProgress: %s"
	english["🦾 <b>ایجنت و ابزارها</b>\n\n🤖 ادامهٔ کار: %s\nقدم‌ها: %d · زمان: %d دقیقه · توقف بی‌کاری: %d دقیقه"] = "🦾 <b>Agent and tools</b>\n\n🤖 Continuation: %s\nSteps: %d · Time: %d min · Idle timeout: %d min"
	english["\n🪟 پنجره context مدل: <b>%s</b> توکن"] = "\n🪟 Model context: <b>%s</b> tokens"
	english["<b>حالت پاسخ</b>: %s\n\n"] = "<b>Response mode</b>: %s\n\n"
	english["⚡ <b>سریع</b>: پاسخ کوتاه و ابزارهای سبک.\n"] = "⚡ <b>Fast</b>: short answers and light tools.\n"
	english["🧠 <b>عمیق</b>: ابزارهای کامل برای کارهای چندمرحله‌ای."] = "🧠 <b>Deep</b>: all tools for multi-step work."
	english["\n🔎 جستجوی وب: treg.to %s"] = "\n🔎 Web search: treg.to %s"
	english["🤖 قدم‌ها در هر دور: %d تا %d\n<code>0</code> = پیش‌فرض سرور"] = "🤖 Steps per round: %d–%d\n<code>0</code> = server default"
	english["🧠 حافظهٔ گفتگو (توکن)\n<code>0</code> = خودکار (%s)\nبازهٔ دستی: %s تا %s"] = "🧠 Conversation memory (tokens)\n<code>0</code> = automatic (%s)\nManual range: %s–%s"
	english["🪟 پنجره context (توکن)\nمثال: <code>256k</code> یا <code>1m</code>\n<code>0</code> = خودکار"] = "🪟 Context window (tokens)\nExample: <code>256k</code> or <code>1m</code>\n<code>0</code> = automatic"
	english["🌐 آدرس API را بفرست:\n<code>https://api.openai.com/v1</code>\n<code>https://api.anthropic.com</code>\nبرای Responses، آدرس کاملِ <code>/responses</code> را بفرست."] = "🌐 Send the API URL:\n<code>https://api.openai.com/v1</code>\n<code>https://api.anthropic.com</code>\nFor Responses, send the complete <code>/responses</code> URL."
}
