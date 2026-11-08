# Macron Agent — server guide

Installation, service management, dependencies, backups and recovery.

## Build and install on Linux

Go 1.26+ is required. Run as the account that should own the agent, in the
extracted source directory:

```sh
make build
./install.sh --binary ./bin/macron --no-setup
# Or build and install the local source directly (no GitHub repository needed):
./install.sh --source-dir "$PWD" --no-setup
```

Optional media/document helpers use Python 3.12+ and FFmpeg. To prepare Python
helpers without modifying the system Python:

```sh
mkdir -p "$HOME/.macron"
chmod 700 "$HOME/.macron"
python3 -m venv "$HOME/.macron/venv"
"$HOME/.macron/venv/bin/python" -m pip install --require-hashes -r requirements-tools.lock
```

`install.sh --tools --binary ./bin/macron --no-setup` also installs supported
system helper packages and the pinned Python environment. The program adds its
private `venv/bin` to PATH at startup, including under systemd. A custom
`MACRON_HOME` uses that home's `venv` instead. Optional browser automation still
needs Playwright and its browser/system libraries separately.

## Run tasks from SSH or cron, without Telegram

Set `MACRON_API_URL`, `MACRON_MODEL`, and, for authenticated providers,
`MACRON_API_KEY` in the environment or private `config.env`. Existing WAZE aliases
are supported. Exported environment settings take precedence over the file.
For example, with an already-running Ollama and a downloaded tool-capable model:

```sh
export MACRON_API_URL=http://127.0.0.1:11434/v1
export MACRON_MODEL=qwen3:8b
macron exec --timeout 5m --max-steps 20 'Create a Python script that checks disk usage and test it'
```

For specialized jobs, reduce the advertised tool schemas explicitly:

```sh
macron exec --tools bash_execute,read_file,write_file,apply_patch,search_code,shell_jobs 'Inspect and test this project'
```

Unknown tool names return exit status 2. This controls the model's tool surface;
shell access still carries the account's privileges and is not a sandbox.

The default CLI now starts with six general shell/file tools and a compact
on-demand catalog. `more_tools` exposes the remaining allowed schemas when needed;
`--all-tools` restores immediate broad advertisement. Explicit `--tools` keeps
its exact selection. Tool discovery preserves the system prefix; adding schemas
can still change provider cache behavior. Existing SOUL.md files are preserved;
the shorter default personality applies to newly created homes.

Run `macron doctor --tools-only` to check executable and Python dependencies
without Telegram or model credentials. `install.sh --browser` downloads Chromium;
`--browser-agent` also installs the optional autonomous runtime in a separate venv.

The command uses the shared agent/tool engine and reads SOUL.md, USER.md,
MEMORY.md and skills. Each invocation uses an independent `workspaces/exec-*`
directory (printed on stderr). The answer goes to stdout. Exit status is 0 for a
completed engine result, 1 for an error, deadline or partial result, and 2 for
invalid arguments. Completion does not independently prove every model claim.
Ctrl-C/SIGTERM cancels the task. Session shell jobs are stopped at command exit;
this command is intended for bounded tasks. Use the existing bot/background-job
workflow for ongoing jobs.

Current boundaries: `exec` is one-shot, does not persist a conversation archive,
does not load the Telegram provider database/failover or Telegram-specific
memory/scheduling/plugin tools. Its shared built-in tools include real filesystem
and command execution with the current user's privileges. The working directory
is an organizational boundary, not a sandbox. It does not require a bot token.
Fully local inference requires a local model server; cloud endpoints still send
requests to that provider. Web/media tools require their respective networks.

## Telegram service

```sh
macron setup
macron doctor
macron service install
macron service status
macron logs -f
```

Use a dedicated ordinary account. Service files now preserve spaces, literal
percent/dollar characters and XML characters in paths, and use UMask=0077 for new
files. This does not restrict the account's host access. Do not install arbitrary
plugins or expose an unauthenticated agent to the Internet.

## Backups and restoration

```sh
macron backup /private/backups/macron.tar.gz
macron backup --all /private/backups/macron-with-workspaces.tar.gz
```

A backup inside MACRON_HOME excludes itself. Only the actual live DB and its
SQLite journal files are replaced with the consistent snapshot; similarly named
user files are kept. Missing or unreadable input is a failure, not a successful
empty/incomplete backup. Existing destination files are never overwritten.
Archives contain credentials and must remain private. Stop the agent before
restoring into the home directory using the restore command printed by backup.

## Validate

```sh
make check
# For optional Python integration tests in a development shell:
PATH="$HOME/.macron/venv/bin:$PATH" make check
# Concurrency checks for the changed Go packages:
go test -race ./pkg/cli ./pkg/config ./pkg/agent ./pkg/machine
```

`make test` also runs the real installer against temporary fake binaries: broken
candidate rollback, previous-version backup, and stale `.new` symlink protection.
The local execution test uses a mock HTTP model, performs a real file write, and
verifies the result returns to the model. It does not measure model intelligence.

## New in 1.2.0: settings at a glance

UPPER_CASE keys live in `config.env`; lower_case keys are the agent's own settings in its database (the panel changes them; `macron config get|set|unset KEY` too). Restart after a change: `macron service restart`.

| Key | Default | What it does |
|---|---|---|
| `auto_continue` | `0` | `0`: a job's budget grows for as long as it makes progress; `N`: at most N extensions; `-1`: a fixed budget (🦾 → ⏩) |
| `idle_timeout` | `15` | minutes with nothing happening (no model reply, no tool progress) before a job stops |
| `approvals.mode` | `smart` | the command gate: `smart` (guardian model, then you), `manual` (always ask), `off` (only the hardline floor) — 🦾 → 🛡 |
| `approvals.denial_breaker_threshold` | `3` | refusals in a row that stop a job (`0` never) |
| `approvals.deny` | — | globs that never run, one per line or a JSON list, e.g. `git push --force*` |
| `approvals.smart_policy` | — | your words for the guardian, e.g. «approve docker compose restarts in ~/deploys» |
| `approvals.checkpoints` | `false` | keep a tar of what a destructive `rm` deletes, in `~/.macron/checkpoints` |
| `MACRON_YOLO` | — | `1` turns the gate off from start (read once; the floor still holds). `/yolo` does it for 30 minutes |
| `learn_auto_adopt` | `true` | a lesson goes into its skill at once (`/learn undo ID` takes it back exactly) |
| `learn_review_interval` | `25` | jobs between two background reviews of the lessons (`0` never) |
| `moa.presets` | — | multi-model presets (JSON list; 🦾 → ⚖️ builds them); `/moa TASK` runs one request through the default one |
| `moa.privacy_filter` | — | `display` or `full`: hide emails and phone numbers in the advisors' text |
| `triage` | `off` | `local` or `model`: decide what a request needs before the model works (🤖 → 🧭 → 🧭 triage) |
| `update_notice` | `true` | tell the owners once per new release (`/update` installs it) |
| `premium_emoji` | `false` | premium emoji (then managed from 🦾 → 🖥) |
| `MACRON_PROFILE` | — | another agent on the same machine: home `~/.macron/profiles/NAME`, service `macron-NAME` |
| `MACRON_WEBHOOK_ADDR` | — | e.g. `:8787`: signed events on `POST /hook/NAME` become background tasks (`/dashboard` shows the secret) |
| `MACRON_DASHBOARD_ADDR` | — | e.g. `127.0.0.1:8788`: a read-only status page (loopback only; `/dashboard` gives the link) |

Commands: `/settings` (the panel; `/admin` still works), `/update`, `/moa`, `/learn undo ID`, `/approve`, `/yolo [off]`, `/board`, `/search WORDS`, `/dashboard`.

### 1.2.1 presentation cleanup

Progress uses the classic code block. Appearance selection, the private-view
selector and the attached final work report are removed. `/mode` selects only
fast/deep responses. Private progress uses a live message with a Stop button underneath;
legacy display preferences are ignored. Internal continuation history is kept.

### 1.2.2: private Telegram and GitHub updates

The bot works only in its owners' private chats. Group/channel messages, guest
messages from groups, edits, reactions and callbacks are ignored. It leaves new
group invitations even from an owner; old group schedules are cancelled when due.
Group command menus and the Privacy-mode instructions are removed.

Speech recognition is removed, including its settings, automatic voice-message
transcription, tool and `whisper-stt` program. Audio files can still be saved,
converted and sent; their spoken content is not read. Music generation and text
to speech remain available.

The owner can use `/update` or Settings → Update in Telegram. Updates install the
latest published GitHub **Release**, not unversioned commits on `main`. For a new
version: change `pkg/app/app.go`, commit and push, then push the matching `vX.Y.Z`
tag. The release workflow builds Linux/macOS amd64/arm64 archives and SHA256SUMS.
The Telegram button downloads the correct archive, checks its hash, runs a
preflight, keeps `macron.old` and restarts the service. Old update buttons cannot
downgrade the running version. A failed restart reports the installed state and
keeps the pending version report for a manual `macron service restart`.

### 1.2.3: asset downloads and CLI context

If a GitHub browser/CDN download URL is unavailable, the updater and installer
try the corresponding public GitHub asset API with `application/octet-stream`.
The SHA-256 and executable preflight checks still apply. Both archive and checksum
downloads were tested against the real v1.2.2 Release in isolated local directories;
no running bot or system service was replaced in this check.

The CLI keeps owner personality/rules and execution/safety instructions, while
loading skill descriptions and hidden tool guidance when needed. Skill discovery
and reading still work, and discovery keeps the system prefix stable. Repeated
model measurements are recorded separately from the 1.2.2 baseline.

### 1.2.4: live private messages and queued follow-ups

Private chat progress is an editable message with the classic code block and an
inline Stop keyboard underneath. It does not use Telegram drafts for ordinary
messages. New messages wait in FIFO order and start separate requests after the
active request finishes. Stopping the active request keeps the waiting tasks.
The queue accepts up to eight waiting messages; a full queue is reported explicitly.


### 1.2.8: native live cards and active-task corrections

Private progress uses native RichMessage blocks: heading, checklist, expandable
execution details with a code block, and an elapsed-time footer. Stop stays in
an inline keyboard below the message. Text answers replace the live card;
files and effects retain their separate delivery paths. Unsupported rich APIs
fall back to the classic HTML card. Premium emoji rejection retries without
custom emoji, and temporary server failures keep rich rendering enabled.

A new plain-text message during a model request cancels only that request and
continues the same task with the correction. During tool execution the guidance
waits for the tool boundary. Messages received during delegation, and attachments,
wait for a separate turn. `/queue TEXT` always requests a separate FIFO turn;
it accepts up to eight waiting tasks and Stop preserves them.

Settings exposes Premium emoji even while disabled: import a pack, add mappings,
preview, toggle, or delete. Reaction controls and their old mutation callbacks
have been retired.

Telegram API references: [InputRichMessage](https://core.telegram.org/bots/api#inputrichmessage),
[sendRichMessage](https://core.telegram.org/bots/api#sendrichmessage),
[editMessageText](https://core.telegram.org/bots/api#editmessagetext).
Persistent cards do not use the draft-only `thinking` block.


### 1.2.9: remove obsolete modules and synchronize command menus

The group administration, membership approval, group history and privacy-warning
modules have been removed along with their unused settings, translated copy and
obsolete tests. Existing database migrations remain so upgrading an old Snap
installation still removes its tables and secrets safely. The old group-message
expiry cleanup is retained solely for databases from previous versions.

All 35 supported private commands and aliases are registered from one bilingual
catalog. Owner, administrator and common menus follow handler access permissions;
unsupported group commands are excluded. Default, English and Persian scopes are
replaced on startup. Owner-menu registration errors now reach startup logging.
An AST-based test checks every private command handler against the owner menu.

The source distribution contains no Snap classifier or speech-recognition program.
See [Telegram commands](docs/telegram-commands.md).
