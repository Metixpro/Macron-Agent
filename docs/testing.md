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

Optional real-model tests use a model service but keep Gmail and messenger transports local. They may incur provider charges. Set these variables in your shell or a private CI secret environment, never in tracked files:

```sh
export MACRON_TEST_LIVE=1
export MACRON_TEST_API_URL=https://your-provider.example/v1
export MACRON_TEST_MODEL=your-tool-capable-model
# Set MACRON_TEST_API_KEY privately in the environment.
go test -count=1 -v ./tests/integration -run '^TestLive'
```

Without `MACRON_TEST_LIVE=1`, live-model tests are explicitly skipped. A live test does not prove delivery to a real Gmail, Telegram or Bale account: those require separate account credentials and an authorized end-to-end check. Do not add credentials, private profiles, transcripts or downloaded release binaries to test fixtures.

برای اجرای تست‌های عمومی، `make test` و برای بررسی race، دستور `make test-race` را بزن. تست‌های عادی به کلید API نیاز ندارند و پیام یا ایمیل واقعی ارسال نمی‌کنند. تست‌های اصلی روی لینوکس و ویندوز در GitHub CI اجرا می‌شوند و نصب‌کنندهٔ ویندوز هم واقعاً نصب و حذف می‌شود. تست مدل واقعی اختیاری است؛ متغیرهای بالا را فقط در محیط خصوصی تنظیم کن. این تست، اتصال به حساب واقعی پیام‌رسان یا Gmail را تأیید نمی‌کند.
