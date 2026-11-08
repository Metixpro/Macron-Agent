# Server development build (1.1.0-dev)

This build adds direct local task execution and fixes concrete installation,
service rendering and backup defects. It is a development build, not a claim of
feature parity or superiority over Hermes.

## Install the uploaded source on Linux

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

Commands: `/settings` (the panel; `/admin` still works), `/update`, `/moa`, `/learn undo ID`, `/approve`, `/yolo [off]`, `/board`, `/search WORDS`, `/dashboard`. Group settings: `/groupsettings`.
