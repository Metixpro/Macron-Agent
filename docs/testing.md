# Tests / تست‌ها

Run the public regression suite from a checkout with Go 1.26.11 or newer:

```sh
make test
make test-race
```

`make check` runs formatting, vet, a production build and the regression suite. `make audit` runs staticcheck and govulncheck. GitHub CI runs the tests on Linux and Windows, the race detector on Linux, a clean installation, and an actual Windows installer build/install/uninstall. The release workflow repeats the installer checks before publishing.

The default suite uses temporary profiles, local HTTP/SMTP/MCP fixtures and scripted model responses. It needs no API key and does not send real mail or messenger messages. It covers MCP reconnects, project isolation and history migration, tool discovery without bulk schema loading, connection metadata and credentials, provider request formats/settings, owner approvals, incoming messenger tool execution, Gmail refresh/read/send, SMTP TLS/cancellation, and updater validation/backup. Windows tests also verify PowerShell labels and the installer removes mixed-case, quoted, trailing-separator and duplicate PATH entries while preserving user data and unrelated entries. SMTP fixture trust through `SSL_CERT_FILE` is Unix-only, so those TLS cases run on Linux.

Some updater tests build a CLI fixture automatically. To reuse an existing build, set `MACRON_TEST_BINARY` to its absolute path. Windows installer checks run only on a disposable Windows runner because they install into the current user's Programs directory:

```powershell
tests/windows/build-installer.ps1
```

The desktop app has Windows end-to-end checks in `tests/desktop`, which CI runs on every push:

- `engine.ps1 -Exe macron.exe [-Gui macron-desktop.exe]` starts the engine with a temporary profile, connects the scripted model in `fake_model.py`, and runs a whole agent job through the engine API: PowerShell with Persian output, a patch without line numbers on a CRLF file, edits, `python3`, a failing command, `run_code`, a background job, sending a file and downloading it. It then stops the engine while a job runs and, with `-Gui`, starts the desktop app itself. CI runs it for x64 and x86.
- `ui.ps1 -Exe macron.exe -Sshd sshd-test.exe` drives the real interface in Microsoft Edge (the engine behind WebView2): the first-run setup, a job with its plan panel and file card, then a server added over SSH (`sshd` is a small local SSH server with the password `pw`), its key check, password, a model connected there, a job there and a download through the connection. On Linux it runs with `MACRON_UI_BROWSER=` (bundled Chromium) and `MACRON_UI_PLAYWRIGHT=playwright`.

Optional real-model tests use a model service but keep Gmail and messenger transports local. They may incur provider charges. Set these variables in your shell or a private CI secret environment, never in tracked files:

```sh
export MACRON_TEST_LIVE=1
export MACRON_TEST_API_URL=https://your-provider.example/v1
export MACRON_TEST_MODEL=your-tool-capable-model
# Set MACRON_TEST_API_KEY privately in the environment.
go test -count=1 -v ./tests/integration -run '^TestLive'
```

Without `MACRON_TEST_LIVE=1`, live-model tests are explicitly skipped. A live test does not prove delivery to a real Gmail, Telegram or Bale account: those require separate account credentials and an authorized end-to-end check. Do not add credentials, private profiles, transcripts or downloaded release binaries to test fixtures.

برای اجرای تست‌های عمومی، `make test` و برای بررسی race، دستور `make test-race` را بزن. تست‌های عادی به کلید API نیاز ندارند و پیام یا ایمیل واقعی ارسال نمی‌کنند. تست‌های اصلی روی لینوکس و ویندوز در GitHub CI اجرا می‌شوند و نصب‌کنندهٔ ویندوز هم واقعاً نصب و حذف می‌شود. برنامهٔ دسکتاپ روی ویندوز هم تست سرتاسری دارد (`tests/desktop`): موتور با یک مدل اسکریپتی یک کار کامل انجام می‌دهد (PowerShell با خروجی فارسی، پچ روی فایل CRLF، پایتون، کار پس‌زمینه، ارسال و دانلود فایل) و خود برنامه باز می‌شود، برای x64 و x86؛ و رابط واقعی در Edge از راه‌اندازی اول تا پنل پلن، کارت فایل و اتصال به سرور از راه SSH امتحان می‌شود. تست مدل واقعی اختیاری است؛ متغیرهای بالا را فقط در محیط خصوصی تنظیم کن. این تست، اتصال به حساب واقعی پیام‌رسان یا Gmail را تأیید نمی‌کند.

## Native Windows command regressions

`go test ./pkg/cli ./pkg/config` checks desktop profiles without config.env, saved encrypted providers, platform install advice, private Python environments and log reading without external programs. The Windows cases actually start, restart and stop a detached process, add and remove a per-profile HKCU startup entry, and reject unrelated/stale process IDs. Every test uses a temporary profile and removes its own startup value.

`tests/windows/cli.ps1 -Exe <absolute-path-to-macron.exe>` runs on Windows PowerShell 5.1 and PowerShell 7 in CI. It checks doctor on a minimal PATH, Persian logs, backup, uninstall dry-run and actual portable uninstall with spaces, Persian and an apostrophe in the installation path. It verifies that `--keep-data` and unrelated files survive. Run it only with a test build; it copies the executable into a temporary installation.

`macron doctor` treats absent optional tools as warnings. Use `macron doctor --tools-only --require-tools` when validating an installation of the pinned Python/document dependencies; missing imports then fail the command.

تست‌های فرمان‌های ویندوز در PowerShell 5.1 و 7 اجرا می‌شوند: doctor بدون ابزارهای لینوکسی، لاگ فارسی، بکاپ و حذف نصب واقعی روی یک کپی موقت. تست Go چرخهٔ پردازش پس‌زمینه و ثبت/حذف شروع هنگام ورود کاربر را هم بررسی می‌کند. نبودن ابزار اختیاری هشدار است؛ برای بررسی سخت‌گیرانهٔ پکیج‌های پایتون، `--require-tools` را اضافه کن.
