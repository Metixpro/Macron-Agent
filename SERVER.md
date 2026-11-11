# Macron Agent — server guide

Installation, service management, dependencies, backups and recovery.

## Installation scope

On Linux or macOS:

```sh
curl -fsSL https://raw.githubusercontent.com/Metixpro/Macron-Agent/main/install.sh | bash
```

The installer asks for the scope before changing files and shows its plan.
Run it as the account that should own the agent. Selecting user scope as root
installs for the root account; it does not switch to another user automatically.

| Scope | Program | Private data | System packages |
|---|---|---|---|
| User | `~/.local/bin/macron` | `~/.macron` | No sudo or package-manager calls |
| System/root | `/usr/local/bin/macron` | `/var/lib/macron` | Supported helpers via apt/dnf |

Python tools are installed by default into the private `venv`. If Python 3.12+
or venv support is missing, the installer provisions a private uv runtime and
Python. Neither needs root. Missing optional native executables are reported in
user scope; FFmpeg and browser system libraries may still need host provisioning.
The program adds its private `venv/bin` to PATH at startup, including in services.
`--browser` downloads a private Chromium; `--browser-agent` also creates a separate
autonomous-browser environment. `--no-tools` skips Python and system packages.

For unattended user installation:

```sh
bash install.sh --scope user --yes --no-setup
macron setup --yes --api-url URL --model NAME     # the model (the key: macron config set MACRON_API_KEY …)
macron telegram setup --yes --token TOKEN --owner ID
```

`--prefix` selects the binary directory, `--home` selects private data,
`--no-service` keeps the Telegram bot from being started in the background. An installation receipt
remembers the scope and home, so later CLI calls and updates use the same paths.

## Build and install from source

Git, Make and Go 1.26+ are required for building, not for release installation:

```sh
make build
./install.sh --scope user --binary ./bin/macron --no-setup
# Or build and install directly from the checkout:
./install.sh --scope user --source-dir "$PWD" --no-setup
```

## Complete removal

```sh
macron uninstall --dry-run
macron uninstall
```

Uninstall shows the paths and asks for confirmation. It stops matching services
and running copies before deleting their unit files, program and backup binaries,
installation receipts, installer-owned PATH entries, and the entire private home.
That includes secrets, conversations, workspaces, profiles and private runtimes.
Run as the installation owner; system installations require root. A failed service
or process stop aborts removal before data is erased.

Use `macron uninstall --keep-data` to retain private data, or `--yes` for unattended
removal. `install.sh --uninstall` delegates to the same command. Shared system
packages and files outside the installation home are not removed. Back up wanted
files first; the default removal cannot be undone.

## Run tasks from SSH or cron, without Telegram

Set `MACRON_API_URL`, `MACRON_MODEL`, and, for authenticated providers,
`MACRON_API_KEY` in the environment or private `config.env`. Exported environment
settings take precedence over the file.
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
macron telegram            # connect the bot, or show its state and actions
macron telegram status
macron telegram restart
macron telegram logs -f
macron doctor
```

`macron telegram` checks the token with Telegram, finds your id from a message
you send the bot, saves both in `config.env`, makes a trial run
(`macron telegram run --check`) and keeps the bot running: as a systemd unit
(`ExecStart=… telegram run`) or a launchd agent, or, where systemd is not running
(containers, WSL without systemd), as a background process recorded in
`telegram.pid` with its log in `macron.log`; that one is started again after a
reboot with `macron telegram start`. `macron service install` still writes the
unit directly. `macron telegram run` runs the bot in the foreground. A bare
`macron` opens the terminal interface when it has a terminal; without one (an
older unit file) it still runs the bot.

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
# Static analysis, known vulnerabilities and every test under the race detector (CI runs this too):
make audit
```

`make test` also checks interactive scope selection, isolated user/system install
paths, Python bootstrap, failure recovery and PATH quoting. Separate lifecycle
tests install a real binary into temporary directories and verify dry-run,
confirmation, full removal and data-preserving removal.
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

Commands: `/settings` (the panel), `/update`, `/moa`, `/learn undo ID`, `/approve`, `/yolo [off]`, `/board`, `/search WORDS`, `/dashboard`.

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
converted and sent; their spoken content is not read. Text to speech remains
available. Music generation was subsequently removed in 1.3.0.

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


### 1.2.10: remove obsolete modules and synchronize command menus

The group administration, membership approval, group history and privacy-warning
modules and unused Telegram moderation wrappers have been removed along with
their unused settings, translated copy and obsolete tests. Existing database migrations remain so upgrading an old Snap
installation still removes its tables and secrets safely. The old group-message
expiry cleanup is retained solely for databases from previous versions.

All 35 supported private commands and aliases are registered from one bilingual
catalog. Owner, administrator and common menus follow handler access permissions;
unsupported group commands are excluded. Default, English and Persian scopes are
replaced on startup. Owner-menu registration errors now reach startup logging.
An AST-based test checks every private command handler against the owner menu.

The source distribution contains no Snap classifier or speech-recognition program.
See [Telegram commands](docs/telegram-commands.md).

### 1.3.0: private installation and complete removal

The installer presents user/system scope before changes, installs Python packages
privately without sudo in user scope, and provisions missing Python/venv support.
`macron uninstall` removes the owned installation and its data after confirmation.
Custom installation homes are retained across CLI calls, services and updates.

AI image generation, AI image editing and music generation are removed, including
their tools, admin panels, bundled skills and credentials. Upgrades remove retired
credential keys from the database and config.env, and retire unmodified old bundled
skills; user-edited skill files are preserved. Web image search, charts, document
processing, existing-audio video composition and text to speech remain available.

Telegram now has 24 canonical commands with no alternate spellings. Use `/language`,
`/settings`, `/new`, `/cookies`, `/reminders` and `/cancel`; choose fast/deep with
`/mode`. See [the current command list](docs/telegram-commands.md).

### 1.3.1: reply context and tool runtime fixes

Private replies use one context path for new requests and corrections during an
active task. The referenced bot message, rich blocks, selected quote and available
attachments reach the model. An explicit quote remains available when Telegram
omits the original body, and a short reply such as “ok” keeps its reference instead
of taking the context-free small-talk path. Explicit `/queue` messages retain the
same reference when their turn starts. Long references use bounded excerpts.

The built-in topic refusal list is removed from the application prompt. Owner
identity checks, secret handling, command approval settings and the boundary
between external content and owner requests remain in place. Model providers
can still enforce their own policies; changing the local prompt does not alter
the provider's behavior. Existing custom SOUL.md content is preserved.

Production binaries now register JPEG and PNG decoders explicitly. Previously,
image search could find and download valid images but fail to recognize them;
test-only imports masked the missing runtime registration. Edge text-to-speech
now combines system/custom trusted CAs with its package trust roots and honors
HTTP(S) proxy environment variables and NO_PROXY, keeping TLS verification on.

### 1.3.2: current requests, queued attachments and Android packages

The current user turn carries its own attachment names while conversation history
remains available. Completed jobs are background, and a fresh upload or a reply to
a different message does not implicitly resume an unrelated unfinished job. Bare
“continue” still resumes the latest unfinished job when no intervening task has moved the conversation on; a resume button or reply can explicitly target an older job. Unrelated turns do not receive old-job hints. Mentioning `continue` in a
new programming question does not.

While a task is running, an explicit new-task message queues a separate turn.
A reference to the most recently queued attachment, or
an explicit reply to a queued attachment, adds instructions to that file's queued
turn without changing FIFO order. Ordinary corrections still steer the active
request; `/queue` always starts a separate turn. Unclear text retains the existing
steering behavior; this is not a semantic guarantee for every possible phrasing.

Uploaded documents keep a per-message/file identity in a private `.incoming` index.
Same-named files no longer overwrite one another; replies and repeated processing
resolve to the original saved copy. A queued file keeps its original payload when
its explanatory text arrives later.

APK, AAB, APKS and XAPK files route to code analysis and can be listed or extracted
with the existing archive tool. Their manifest, DEX and assets can be inspected as
data; uploading a package does not install or run it on the server. Binary Android
XML and bytecode may require separate inspection tools for deeper analysis.

The software-update entry in Settings is labelled **Update** in English.

### 1.3.4: the owner is asked before a job touches the agent itself

The agent does whatever its owner asks on the server; none of this refuses. A page,
file or message it reads can also try to instruct it, so the command gate now asks
the owner first (one tap) when a job is about to act on the agent itself or carry a
file off the machine. With `approvals.mode off` or `/yolo` nothing is asked, as for
every other command; scheduled and background jobs have nobody to ask, so there
these are refused and reported.

- `config.env` and `secret.key`: reading, changing, copying or deleting them, from
  the shell, from `run_code` in any language, and from `read_file`, `write_file`
  and `edit_file` (also through a link).
- The `macron` program: `uninstall`, `service stop|restart|install`, `update`,
  `backup`, `exec`, and `config set|unset` (the gate's own settings stay on the
  floor, as before).
- Copying the whole home folder or the database; removing or moving the home folder;
  `find … -delete` and `mv` on system and home folders; `kill $(pgrep macron)`.
- `curl`/`wget` uploads of a file from outside the work folder, and a download piped
  into `python`, `node`, `perl` and the like.
- `write_file`, `edit_file` and `run_code` writing outside the work folder pass the
  gate like the shell command that would write the same file.

These questions are the owner's alone: the guardian model is not consulted for them.

The owner's prompt carries no rule that keeps anything from them. The answers kept
for other readers («جزئیات فنی پیش خودمون می‌مونه») and the check that withheld an
answer naming tools now apply only to guest answers shown in other chats; they no
longer reach the owner's casual messages, scheduled jobs or `macron exec`.

Settings and tool variables are named `MACRON_…` only. A `config.env` or service
environment that still uses the names from before the rename is read once and
rewritten with the new ones. Plugins receive `MACRON_PLUGIN`, `MACRON_TOOL` and
`MACRON_SESSION`; a script plugin's header is `# plugin:` or `# macron-plugin:`.

Also in this release:

- A model's streamed answer is no longer cut after three minutes; only a silent
  stream is (the provider's idle limit).
- A crash inside a scheduled job, a background task, a tool's parallel workers or a
  housekeeping loop is logged and reported as that job's failure instead of ending
  the whole agent; long-lived loops start again.
- `macron config set` refuses a value with a line break.
- The installer takes the Python lockfiles from the release archive it verified.
- Release builds use Go 1.26.10 (32 standard-library advisories fixed since 1.26.0).

The main branch no longer includes test files or explanatory source comments.
Build directives and module dependency annotations remain where required.

### 1.4.0: exact work records, cached history and Telegram widgets

Each reply now keeps an exact record of its work for the next request: the files
it changed, and every step with its outcome (a command's exit code and last output
line, what an edit replaced, what a download saved, a syntax warning). Look-ups are
grouped and a long job is shortened, never its file list. "Continue" and follow-up
requests build on this record instead of listing folders and reading files again to
find out what was done. An unfinished job points at its record instead of repeating
it; an in-job context summary carries the exact step record too.

Only the latest few replies keep their full steps; older ones keep their file list,
thinned three at a time so the cached part of the request stays the same between
turns. The long-term memory summary keeps a "Work so far" section with paths,
servers and what is left.

`bash_execute` returns a terminal transcript (`$ command`, output, `[stderr]`,
`[exit N · time]`) like `run_code`, instead of indented JSON: fewer tokens, and
failed commands are now marked as failed in the live card and the tool statistics.

With Anthropic, the end of the conversation history is a cache breakpoint, so a new
request reads the earlier conversation from the cache. Through OpenRouter, Claude
models get the same cache markers.

Telegram widgets (Bot API 10.4): the agent can put an interactive mini app inside
a message — a dashboard, a form, a calculator, a game, a live status panel — with
`send_widget`, and push new data into it with `update_widget`. A widget is one HTML
document with inline CSS and JavaScript (no network, at most 32 KB and 4 KB
compressed); `send_widget` measures it before sending. A widget's requests
(`TelegramWidget.request`) are answered by a handler script in the workspace
(`.py`, `.js`, `.sh`…) that runs on this server, so a tap needs no model call; it
can also update the widget's data by printing `{"answer", "chat_data",
"public_data"}`. Failures reach the widget as `{"error": …}` and are logged in
`.widgets/N.log`. Replying to a widget tells the agent which one it is and the
files it was made from. The built-in `telegram-widgets` skill teaches the format.
Widgets need a Telegram server with Bot API 10.4 (a local Bot API server must be
updated); in production they work in private chats, which is where Macron runs.


### 1.5.0: the terminal interface and `macron telegram`

`macron` with no arguments opens an interactive terminal interface in the current
folder: streamed Markdown answers, each tool step as it happens (a command with its
last output lines and exit status, a file written with its first lines, an edit as a
diff), the plan as a checklist, sub-agents with their progress, and an approval
prompt for flagged commands (once, for this session, always, or no — which also
stops the turn). Esc interrupts; messages typed while it works are handed to it at
its next step. Slash commands: `/model` (a filterable list from the provider),
`/approvals`, `/yolo`, `/new`, `/clear`, `/compact`, `/status`, `/init` (writes
`MACRON.md`), `/telegram` (runs the Telegram setup in place) and `/exit`.

The folder is the workspace: tools use relative paths from it, and its `MACRON.md`,
`AGENTS.md` or `CLAUDE.md` is read every turn. Each folder has its own
conversation, kept in the database with the same exact work records as Telegram,
so running `macron` there again continues the work without re-reading files. The
agent's scratch folders (`.agents/`, `.jobs/`, `.run/`…) are added to the
repository's `.git/info/exclude`. The first run asks for a model when none is set.

`macron telegram` connects the bot step by step and keeps it running (see
"Telegram service"). `macron setup` now sets up the model and the time zone and
only offers Telegram; the installer does the same, and starts the bot only when a
token is configured. Systemd units and launchd agents run `macron telegram run`.

### 1.6.0: approval gates for every action, a sandbox, and new tools

Built on PR #1 by ArtixPortal, reviewed and reworked: what was kept was fixed until
it works and is honest, and what was only a sketch was left out.

Approvals now cover more than shell commands. Reading private keys and
credentials (`~/.ssh`, cloud and tool credentials, `/etc/shadow`, process
environments), reaching cloud metadata services and destroying git history
(force push, `reset --hard`, `clean -f`, history rewrites) are flagged. Browser
actions that act on a site (sending a form, pressing a buy, send or delete button,
uploading, page scripts) are asked first, as are plugin and MCP tools that are not
read-only, `send_file` for a file outside the workspace, the Python kernel, the
deploy tool and every tool that sends something out (email, messengers, cloud
uploads, task boards, GitHub writes, OpenAPI calls other than GET). `macron exec`
uses the same gate and settings: it asks in a terminal and refuses flagged steps
when nobody can answer (cron, pipes) unless `--yolo` or `MACRON_YOLO=1`.

`MACRON_SANDBOX=bwrap` runs commands in a bubblewrap container: the system is
read-only, private keys and the agent's own settings, key and database are hidden,
other sessions' workspaces are invisible, and only the workspace (with the terminal
interface, the project folder) and build caches (`~/.cache`, `~/go`, `~/.npm`,
`~/.cargo`…) are writable. `MACRON_SANDBOX_NO_NET=1` also cuts the network.

A question the agent asks with a poll now survives a restart (kept in the
database for the poll's lifetime). Before saying it is done after changing code,
the agent checks it once (tests, a build, or a run without side effects) when
nothing has run since the change.

New tools, each loaded only when a message asks for it:
- `tsetmc_stock` (prices, volume, EPS, best bid/ask and Codal letters from TSETMC,
  or the indices), `price_alert`, `page_watch` (both check when called; schedule a
  recurring check for alerts) and `generate_briefing`.
- `office_document` (Word and PowerPoint with python-docx/python-pptx, right to left
  for Persian, template filling), `ocr_image` (tesseract, then the vision model),
  `translate_document` (long text files in parts), `generate_diagram` (Graphviz,
  mermaid-cli), `document_diff`, `media_summary` (from a transcript),
  `generate_image` (`MACRON_IMAGE_API_KEY`).
- `database_query` (one read-only statement in a read-only transaction; the
  connection string comes from a `config.env` variable), `python_kernel`,
  `create_dashboard`, `statistics_forecast`, `openapi_call` (a bearer token from a
  `config.env` variable), `deploy_service`, `live_preview`, `crawl_site`,
  `rag_search`, `academic_search` (arXiv, Semantic Scholar), `fact_check`.
- `email_client` (SMTP), `calendar_client`, `iran_messenger` (Bale, Eitaa, Rubika
  bots), `external_messenger` (Slack, Discord, WhatsApp Cloud API),
  `cloud_storage` (S3 with SigV4 for AWS, MinIO and R2, and WebDAV), `task_board`
  (Notion, Trello, Todoist), `github_workflow`.

Their keys go in `config.env` (see `config.env.example`); tools never take a token
or password as a parameter. When a service does not answer, the tool says so; it
never fills in made-up numbers.

`MACRON_API_ADDR=127.0.0.1:8789` serves an OpenAI-compatible API on this machine
(`/v1/chat/completions` with streaming, `/v1/models`) that answers with your
providers and their failover; model `macron` or `macron/<provider>`. It needs the
key `/dashboard` shows.


Builds use Go 1.26.11, which fixes a crash of HTTP servers (GO-2026-6859). New dependencies: the pgx (PostgreSQL) and MySQL drivers.

### 1.6.1: commands keep working when the database refuses

When the database refused a write (a full disk, or another program holding it locked), every message, commands included, got only «Something went wrong». The owner now sees the database error itself, and commands such as /update and /settings still run. A crash while handling a message also shows the owner its cause.
