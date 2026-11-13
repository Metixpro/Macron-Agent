<div align="center">

<img src="assets/macron-banner.svg" alt="Macron Agent" width="100%">

# Macron Agent

**Your personal agent, on your own server.**

Write code · Automate work · Create files · Keep the conversation going

[![Release](https://img.shields.io/github/v/release/Metixpro/Macron-Agent?color=55c9a5)](https://github.com/Metixpro/Macron-Agent/releases/latest)
[![CI](https://github.com/Metixpro/Macron-Agent/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/Metixpro/Macron-Agent/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-55c9a5)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white)](go.mod)

[فارسی](README.md) · [Server guide](SERVER.md) · [Downloads](https://github.com/Metixpro/Macron-Agent/releases/latest)

</div>

Macron is a personal Go agent that uses real tools to work on code, files, documents, the web and your server. Choose your model and keep control of your memory, files and configuration.

## Install

On Linux or macOS:

```bash
curl -fsSL https://raw.githubusercontent.com/Metixpro/Macron-Agent/main/install.sh | bash
```

Choose **current user** or **system/root** first. The installer shows the paths, verifies the download and sets up the model; connecting Telegram is optional, there or later with `macron telegram`.

User installations need no `sudo`. Python packages go into a private environment; a compatible Python is provisioned when needed. Missing optional system executables are reported.

### In the terminal

In any project folder, just run:

```bash
macron
```

An interactive terminal interface opens: answers stream in rendered Markdown, every step (a command with its last output lines, a file written, an edit's diff) is listed as it happens, the plan's checklist updates, and risky commands wait for your approval in a prompt. Macron works in that folder and follows the project's `MACRON.md` (or `AGENTS.md`). Each folder keeps its conversation: run `macron` there again and it continues with the exact record of what was done.

| Key or command | Does |
|---|---|
| `Enter` · `Ctrl+J` | send · new line |
| `Esc` | interrupt |
| `Ctrl+P` | search commands in a bounded, scrollable menu |
| `PageUp` · `PageDown` · `Ctrl+End` | browse history · follow the latest output |
| `/provider` | switch the active provider |
| `Tab` · `Shift+Tab` | switch Build / Plan; Plan exposes only read-only tools |
| `/connect` · `/connections` | connect a model or service · inspect setup without exposing secrets |
| `/model` · `/approvals` · `/yolo` | switch the model · how commands are approved · no prompts for 30 minutes |
| `/new` · `/compact` · `/status` | fresh conversation · summarize the history · status |
| `/init` | write `MACRON.md` for the project |
| `@path` | point Macron at a project file; `Tab` completes the path |
| `!command` | run a shell command yourself; Macron sees its output with your next message |
| `/copy` · `/export` | copy the last answer to the clipboard · save the conversation as Markdown |
| `/theme` | colors for a dark or a light terminal (`dark` · `light` · `auto`) |
| `/telegram` | connect and manage the Telegram bot |

The conversation keeps its composer and colored status bar at the bottom while history scrolls separately. Persian/Arabic text is shaped and reordered for display; logical input sent to the model is preserved. If Persian shows reversed (terminals that order right-to-left text themselves, such as most mobile SSH apps, Konsole and GNOME Terminal), type `/rtl`; the choice is remembered.

The welcome screen shows the active model and mode and adapts to narrow terminals. Use `/connect model` to configure a provider. Ask in chat to connect Bale, inspect Gmail or configure another service: Macron discovers the connection tool, checks existing settings and asks only for what is missing. Saving credentials and verifying authentication are separate; connection checks do not send messages.

See [Messenger chat and email](docs/messengers-email.md) for Bale/Rubika tool execution, the Eitaayar limitation and Gmail setup.

### In Telegram

```bash
macron telegram
```

It walks through the bot token (from @BotFather) and your id (send the bot a message and it sees it), saves the settings and keeps the bot running in the background: as a systemd or launchd service that starts at boot, or as a background process where there is no system service. Later, the same command shows its state and offers restart, stop, the log and changing the bot. `macron telegram status|start|stop|restart|logs -f` work too.

Talk to the bot in a **private chat**. It answers configured owners only and does not operate in groups or channels. Progress appears in a native rich card with a checklist, expandable execution details, code blocks and a **Stop** button underneath. The final answer replaces the card. New text guides the active task; use `/queue next task` for a separate turn. Manage premium emoji packs and mappings under **Settings → Premium emoji**.

## Capabilities

| Area | Tools |
|---|---|
| Code and servers | Project inspection, file editing, shell/code execution, tests, Git and process management |
| Files and data | PDF, Word, Excel, CSV, charts, archives and data processing |
| Web | Search, page reading, downloads and browser automation |
| Memory and skills | Persistent conversations, history search, editable skills and learning |
| Background work | Scheduling, background tasks, task board and helper agents |
| Models and extensions | Custom providers, routing, multiple models, plugins and MCP |
| Telegram widgets | Interactive dashboards, forms, calculators and games inside messages, answered by scripts on your server |
| Markets | Tehran Stock Exchange prices, volume, EPS, order book and Codal letters per symbol, the indices, price alerts and a morning briefing |
| Documents and media | Real Word and PowerPoint files (right-to-left aware), Persian OCR, long-file translation, diagrams, document comparison, minutes from a transcript |
| Data and development | Read-only queries on PostgreSQL, MySQL and SQLite, a persistent Python kernel, HTML dashboards, statistics and forecasts, OpenAPI calls, deploy with rollback |
| Connections | Email, .ics calendar events, Bale, Eitaa, Rubika, Slack, Discord, WhatsApp, S3 and WebDAV, Notion, Trello, Todoist and GitHub; anything sent out is asked first |

Some capabilities need additional packages or service credentials. Check your installation with:

```bash
macron doctor --tools-only
```

## Run one task without the interface

For scripts and cron:

```bash
macron exec "Inspect this project and run its tests"
```

Configure the model through setup or `MACRON_API_URL`, `MACRON_MODEL` and `MACRON_API_KEY`. Each invocation gets its own workspace, whose path is printed to the terminal. OpenAI-compatible endpoints are supported, including local model servers.

## Telegram commands

| Command | Purpose |
|---|---|
| `/start` | Start and introduction |
| `/help` | Help |
| `/queue` | Queue a separate task |
| `/new` | New conversation |
| `/memory` | Memory and files |
| `/mode` | Choose response mode |
| `/files` | Manage files |
| `/cookies` | Manage download cookies |
| `/reminders` | Reminders and schedules |
| `/tasks` | Background tasks |
| `/search` | Search past conversations |
| `/board` | Task board |
| `/dashboard` | Web dashboard |
| `/settings` | Administration panel |
| `/update` | Update from GitHub |
| `/learn` | Manage learning |
| `/teach` | Teach a lesson |
| `/moa` | Multi-model voting |
| `/approve` | Approve a pending command |
| `/yolo` | Configure command approval |
| `/language` | Bot language |
| `/id` | User and chat IDs |
| `/me` | Account and usage |
| `/cancel` | Stop the current task |

## Your memory, your files

User installations default to `~/.macron`; system installations use `/var/lib/macron`. Choose another directory with `--home`:

| Path | Contents |
|---|---|
| `SOUL.md` | Personality and response style |
| `USER.md` | Lasting owner information |
| `MEMORY.md` | Notes |
| `skills/` | Procedures and skills |
| `workspaces/` | Working files and outputs |

## Build from source

Requires Git, Make and **Go 1.26+**:

```bash
git clone https://github.com/Metixpro/Macron-Agent.git
cd Macron-Agent
make build
./bin/macron
```

To prepare tools and browser support from the checkout:

```bash
./install.sh --binary ./bin/macron --tools --browser-agent --no-setup
```

## Service and updates

```bash
macron telegram status
macron telegram restart
macron update
```

You can also use `/update` or the admin panel's Update button. Downloads come from GitHub Releases, with checksum verification, executable preflight and a retained previous binary. To publish a version, keep the source version and `vX.Y.Z` tag aligned.

## Uninstall

```bash
macron uninstall --dry-run
macron uninstall
```

After confirmation, uninstall stops this installation’s services and processes, then removes the program, configuration, keys, memory, workspaces and private Python environments. Use `--keep-data` to preserve data or `--yes` to skip the question. Shared system packages remain installed.

## Development

```bash
make check
make audit   # static analysis, known vulnerabilities, race detector
make build
```

The agent runs with its account's permissions. Keep tokens, keys, cookies and backups out of public repositories. See the [server guide](SERVER.md) for installation, services, dependencies and recovery.

## License

[MIT](LICENSE). Free to use, modify and distribute with the license notice retained.

Messenger chat and Gmail OAuth setup: [connection guide](docs/messengers-email.md).
