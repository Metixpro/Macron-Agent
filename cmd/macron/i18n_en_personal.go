package main

// English for the personal edition's own texts (the memory files).

func init() {
	for fa, en := range map[string]string{
		"\n\nℹ️ مدل معمولی همون پروایدر فعاله (⭐ توی 🤖 پروایدرها).": "\n\nℹ️ The regular model is the active provider (⭐ in 🤖 Providers).",
		"\n▫️ معمولی: %s\n": "\n▫️ Regular: %s\n",
		"<b>%s</b>\n\n🔋 توکن امروز: <b>%s</b>\n🧠 حافظه گفتگو: %s\n📌 چیزهایی که درباره‌ات یادمه: %s\n🗂 گفتگوهای بایگانی‌شده: %s پیام\n📁 فایل‌ها: %d (%s)": "<b>%s</b>\n\n🔋 Tokens today: <b>%s</b>\n🧠 Conversation memory: %s\n📌 What I remember about you: %s\n🗂 Archived conversations: %s messages\n📁 Files: %d (%s)",
		"▫️ <b>مدل معمولی</b>\n\nهمه درخواست‌ها، جز کارهای سنگین، اول به این پروایدر می‌رن. «خودکار» یعنی ⭐ فعال پنل.":                                   "▫️ <b>Regular model</b>\n\nEvery request except the heavy jobs goes to this provider first. «Automatic» means the panel's ⭐ active one.",
		"▫️ مدل معمولی":                     "▫️ Regular model",
		"▫️ مدل معمولی (مسیریابی خاموشه)\n": "▫️ Regular model (routing is off)\n",
		"▫️ مدل معمولی — %s\n":              "▫️ Regular model — %s\n",
		"💪 <b>مدل قوی</b>\n\nکارهای سنگین اول به این پروایدر می‌رن (اگه خطا بده، بقیه جاش جواب می‌دن). «هیچ» یعنی مسیریابی خاموش و همه چیز با مدل معمولی.": "💪 <b>Strong model</b>\n\nHeavy jobs go to this provider first (if it fails, the others answer in its place). «None» turns routing off: everything goes to the regular model.",
		"💪 قوی: %s\n": "💪 Strong: %s\n",
		"💪 قوی: %s ⚠️ خاموشه؛ تا روشن نشه همه چیز با مدل معمولیه\n": "💪 Strong: %s ⚠️ it is off; until it is on everything goes to the regular model\n",
		"💪 قوی: %s ⚠️ همون مدل معمولیه\n":                           "💪 Strong: %s ⚠️ it is the regular model\n",
		"💪 قوی: انتخاب نشده (همه چیز با مدل معمولی)\n":              "💪 Strong: not chosen (everything goes to the regular model)\n",
		"💪 مدل قوی — %s\n": "💪 Strong model — %s\n",
		personalWelcome: `Hi %s, %s

I'm <b>Macron</b>, your personal assistant living on your own server 🏠
I do whatever a person at a computer can: search, files, code, servers, downloads, pictures and songs… I have full access to this very machine, so I can install programs, bring services up and edit the server's files.

<b>Everything I learn is yours</b>
• My personality is a file (SOUL.md), what I know about you is another (USER.md) and my notes on the work and the server a third (MEMORY.md). Edit them anywhere and the change counts from the next message.
• What I know how to do are «skills»; after hard jobs I take a lesson and do them better next time.
• Every conversation of ours is archived for good; ask «what was that server we talked about last year?»

<b>Commands</b>
/new — a fresh conversation
/memory — my memory and files
/files — my files
/reminders — reminders and scheduled jobs
/tasks — background jobs
/settings — the panel: models, search, learning and settings
/language — language (فارسی / English)
/help — help`,
		personalHelp: `<b>How to work with me</b>

Write the way you talk to a colleague. I work out what is needed: search, files, code, commands on the server, downloads, pictures, charts, PDFs…

• <b>Server:</b> «restart the nginx service», «why is the disk full?», «write a Python Telegram bot and bring it up». I have full access and show my steps as I go.
• Send <b>files and pictures</b> and I work on them. <b>Reply</b> to any message and I know which one you mean.
• <b>Reminders and scheduled jobs:</b> «every day at 8 send me the server status». List and cancel with /reminders
• <b>Background jobs:</b> long jobs go to the background and their result comes separately (/tasks).
• <b>Memory:</b> «remember …» is saved in USER.md or MEMORY.md. See and delete with /memory, or edit the files in my home folder.
• <b>The past:</b> every conversation we had is searchable; say «remember what we said about …?»
• <b>Connecting:</b> say it in the chat: a server (SSH), GitHub, an MCP server, a plugin or a new skill; «connect this server: address, user, password or key». I connect it myself and show you.
• <b>Learning:</b> from jobs that first break and then work I take lessons and write them into my skills; skills are files in the <code>skills</code> folder of my home, and you can see and edit them too.

/new — a fresh conversation
/memory — memory and files
/mode — answer mode (⚡ fast / 🧠 deep)
/files — my files
/reminders — reminders
/tasks — background jobs
/settings — the panel
/language — language (فارسی / English)
/cancel — stop the current job`,
		personalHomeFmt: `

🤖 Active provider: %s
⏰ Active schedules: %d reminders · %d jobs
💬 Archived conversations: %s messages
📁 Home folder: <code>%s</code>
⏱ Online for %s`,
		"حافظه و فایل‌هام (SOUL.md، USER.md، MEMORY.md)":         "My memory and files (SOUL.md, USER.md, MEMORY.md)",
		"🧠 <b>یه درس تازه یاد گرفتم</b> و توی مهارت‌هام نوشتمش:": "🧠 <b>I learned a new lesson</b> and wrote it into my skills:",
		"↩️ پس بگیر":                                "↩️ Take it back",
		"👁 دیدن و ویرایش":                           "👁 See and edit",
		"\n🧑 <b>درباره تو</b> (USER.md)":            "\n🧑 <b>About you</b> (USER.md)",
		"\n🛠 <b>درباره کار و سیستم</b> (MEMORY.md)": "\n🛠 <b>About your work and machine</b> (MEMORY.md)",
		"زبان: فارسی / English":                     "Language: Persian / English",
		"\n⏳ فایل\u200cها %s بعد از آخرین تغییر خودکار پاک می\u200cشن.": "\n⏳ Files are deleted %s after their last change.",
		"\n\nیه دسته رو انتخاب کن 👇":                                    "\n\nPick a category 👇",
		"📊 <b>آمار کامل</b>\n\n<b>پیام‌ها</b>\n• امروز: <b>%d</b> · ۷ روز: %d · کل: %d\n\n<b>مدل زبانی (امروز)</b>\n• توکن ورودی: %s · خروجی: %s\n• پروایدرها: %d (فعال: %d)\n\n<b>حافظه و فایل</b>\n• پیام‌های توی پنجره گفتگو: %d\n• فایل‌های من: %d (%s)%s\n• دیتابیس: %s\n\n<b>سیستم</b>\n• %s\n• رم ربات: %s · گوروتین: %d": "📊 <b>Full stats</b>\n\n<b>Messages</b>\n• Today: <b>%d</b> · 7 days: %d · total: %d\n\n<b>Language model (today)</b>\n• Input tokens: %s · output: %s\n• Providers: %d (enabled: %d)\n\n<b>Memory and files</b>\n• Messages in the conversation window: %d\n• My files: %d (%s)%s\n• Database: %s\n\n<b>System</b>\n• %s\n• Bot RAM: %s · goroutines: %d",
		"📄 فایل‌ها":                          "📄 Files",
		"🧬 روح من: شخصیت و لحن":              "🧬 My soul: personality and tone",
		"🧑 اینا رو درباره تو می‌دونم":        "🧑 What I know about you",
		"🛠 اینا رو از کار و سیستم یاد گرفتم": "🛠 What I learned about your work and machine",
		"\n\n📄 همه‌اش فایله: SOUL.md، USER.md و MEMORY.md توی پوشه خونه‌ام. با «📄 فایل‌ها» می‌گیریشون، و هر جا ویرایششون کنی از پیام بعدی اعمال می‌شن.": "\n\n📄 It is all files: SOUL.md, USER.md and MEMORY.md in my home folder. Get them with «📄 Files», and edit them anywhere: the change counts from the next message.",
	} {
		english[fa] = en
	}
}
