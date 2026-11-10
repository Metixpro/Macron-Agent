package agent

import (
	"strings"

	"github.com/Metixpro/macron-agent/pkg/tools"
)

type capability struct {
	line  string
	tools []string
}

var capabilities = []capability{
	{"🖼 find real photos on the web", []string{"search_image"}},
	{"🔎 search the web and read sites and news, with sources", []string{"web_search", "web_fetch"}},
	{"🧠 deep, multi-step research with a full cited report", []string{"deep_research"}},
	{"🎵 find and send songs; download video and audio from YouTube, Instagram, TikTok, SoundCloud and direct links", []string{tools.ToolDownloadMedia, "search_music", "download_file"}},
	{"📊 charts, QR codes, text to speech (also as a voice message), converting, cutting and compressing audio and video, GIFs, Telegram voice and round video messages", []string{"generate_chart", "generate_qr", "text_to_speech", "media_convert"}},
	{"📄 read and make PDFs, merge, cut, rotate and compress them, turn pages into pictures and photos or scans into a PDF; read Word, Excel and PowerPoint files and SQLite databases; answer from books of hundreds of pages, citing the pages", []string{"pdf_tool", tools.ToolDocumentReader}},
	{"📈 analyze Excel, CSV, JSON and database files with SQL (sums, filters, joins across files) and make real Excel files", []string{tools.ToolQueryData, tools.ToolCreateSpreadsheet}},
	{"📦 unpack zip, rar, 7z, tar and more (with their passwords) and pack files into zip or 7z, password-protected or in parts for Telegram", []string{tools.ToolExtract, tools.ToolPackFiles}},
	{"👨‍💻 run real code (Python, JavaScript, Go, Rust, C/C++…), find bugs and hand back the fixed file", []string{tools.ToolRunCode}},
	{"🔌 connect extra tools yourself when the owner asks in the chat: MCP servers and plugins, with their tokens", []string{"plugins"}},
	{"🌐 use websites like a person: open, click, fill forms, log in, take screenshots and PDFs", []string{tools.ToolBrowser}},
	{"🤖 hand a whole job on a website to a browser agent that browses on its own (search, compare, go through many pages, long forms) and reports", []string{tools.ToolBrowserAgent}},
	{"🎬 motion graphics and animations as video (titles, intros, logos, infographics), with an optional existing audio track", []string{tools.ToolMotion}},
	{"⏰ reminders and scheduled tasks («send the gold price every day at 10»)", []string{"schedule"}},
	{"🧠 remember what the user tells about themselves (they see and delete it with /memory)", []string{"remember"}},
	{"🎲 dice and games, polls and quizzes, pins on the map", []string{"roll_dice", "create_poll", "send_location"}},
	{"🧩 interactive widgets inside the chat: dashboards, forms, calculators, games and live status panels that ask this server for data", []string{"send_widget"}},
	{"💱 live dollar, euro, dirham, gold, mithqal and coin prices in Toman; cryptocurrency prices; domain and GitHub details", []string{tools.ToolMarketPrice, "crypto_currency_price", "whois_lookup", "github_inspect"}},
	{"🪪 look up a Telegram id, @username or link: name, bio, premium, profile photos, data center, roughly when the account was made, a group's or channel's members", []string{"telegram_lookup"}},
}

func capabilityList(offers func(string) bool) string {
	var sb strings.Builder
	sb.WriteString("What you can do right now (offer and promise only these; when something asked for is not here, say so plainly " +
		"and offer what is):\n")
	for _, c := range capabilities {
		for _, t := range c.tools {
			if offers(t) {
				sb.WriteString("- " + c.line + "\n")
				break
			}
		}
	}
	return sb.String()
}
