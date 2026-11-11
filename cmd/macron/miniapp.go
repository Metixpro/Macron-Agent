package main

import (
	"net/http"

	"github.com/Metixpro/macron-agent/pkg/telegram"
)

// MiniAppHTML serves the embedded interactive Telegram Mini App frontend.
const MiniAppHTML = `<!DOCTYPE html>
<html lang="fa" dir="rtl">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0, user-scalable=no">
  <title>Macron Agent Mini App</title>
  <script src="https://telegram.org/js/telegram-web-app.js"></script>
  <style>
    :root {
      --bg: var(--tg-theme-bg-color, #0f172a);
      --text: var(--tg-theme-text-color, #f8fafc);
      --card: var(--tg-theme-secondary-bg-color, #1e293b);
      --btn: var(--tg-theme-button-color, #6366f1);
      --btn-text: var(--tg-theme-button-text-color, #ffffff);
    }
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body { font-family: system-ui, -apple-system, sans-serif; background: var(--bg); color: var(--text); padding: 1.25rem; }
    .header { text-align: center; margin-bottom: 1.5rem; }
    .header h1 { font-size: 1.5rem; margin-bottom: 0.25rem; }
    .badge { display: inline-block; background: rgba(16, 185, 129, 0.2); color: #10b981; padding: 0.25rem 0.75rem; border-radius: 999px; font-size: 0.75rem; font-weight: bold; }
    .grid { display: grid; gap: 1rem; margin-bottom: 1.5rem; }
    .card { background: var(--card); border-radius: 12px; padding: 1rem; }
    .card h3 { font-size: 1rem; margin-bottom: 0.5rem; opacity: 0.9; }
    .btn { display: block; width: 100%; background: var(--btn); color: var(--btn-text); border: none; border-radius: 8px; padding: 0.75rem; font-size: 0.95rem; font-weight: 600; cursor: pointer; text-align: center; margin-top: 0.5rem; }
  </style>
</head>
<body>
  <div class="header">
    <h1>🤖 Macron Agent</h1>
    <span class="badge">فعال و آماده‌به‌کار</span>
  </div>

  <div class="grid">
    <div class="card">
      <h3>🚀 کارهای فعال</h3>
      <p style="font-size: 0.875rem; opacity: 0.8;">مدیریت لحظه‌ای برنامه‌ها، زیرعامل‌ها و خروجی‌های فضای کاری.</p>
      <button class="btn" onclick="sendAction('/status')">مشاهده وضعیت سیستم</button>
    </div>

    <div class="card">
      <h3>📁 فایل‌ها و اسناد</h3>
      <p style="font-size: 0.875rem; opacity: 0.8;">دسترسی سریع به اسناد تولید شده، گزارش‌ها و فایل‌های پروژه.</p>
      <button class="btn" onclick="sendAction('/compact')">بهینه‌سازی حافظه</button>
    </div>
  </div>

  <button class="btn" style="background: rgba(239, 68, 68, 0.2); color: #ef4444;" onclick="closeApp()">بستن مینی‌اپ</button>

  <script>
    if (window.Telegram && window.Telegram.WebApp) {
      const tg = window.Telegram.WebApp;
      tg.ready();
      tg.expand();
    }
    function sendAction(cmd) {
      if (window.Telegram && window.Telegram.WebApp) {
        window.Telegram.WebApp.sendData(cmd);
      } else {
        alert("Command: " + cmd);
      }
    }
    function closeApp() {
      if (window.Telegram && window.Telegram.WebApp) {
        window.Telegram.WebApp.close();
      }
    }
  </script>
</body>
</html>`

// ServeMiniApp handles HTTP requests to the /miniapp endpoint.
func (b *Bot) ServeMiniApp(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(MiniAppHTML))
}

// MiniAppKeyboard generates an inline button to open the Telegram Mini App.
func (b *Bot) MiniAppButton(text, miniappURL string) telegram.InlineKeyboardButton {
	return telegram.InlineKeyboardButton{
		Text: text,
		WebApp: &telegram.WebAppInfo{
			URL: miniappURL,
		},
	}
}
