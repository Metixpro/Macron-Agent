<div align="center">

# Macron Agent — مکرون بهتر از هرمس (Macron, better than Hermes)
### A personal agent that lives on your server and does real work

[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](go.mod)
[![CI](https://github.com/Metixpro/Macron-Agent/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/Metixpro/Macron-Agent/actions/workflows/ci.yml)
![Storage](https://img.shields.io/badge/storage-SQLite-003B57?logo=sqlite)
![Status](https://img.shields.io/badge/status-development-orange)

**Self-hosted · Telegram · File-based memory · Real tools · Your model**

[فارسی](README.md) · [Server guide](SERVER.md) · [Validation](VALIDATION.md) · [Roadmap](ENGINEERING_ROADMAP.md)

</div>

Macron is a personal Go agent for coding, files, documents, web research and server tasks. Its Telegram workflow adds persistent conversations, memory, skills, scheduled tasks and background jobs. Personality and lasting knowledge are editable files; SQLite holds application state.

**Development build.** Source installation on Linux is the recommended test path. Downloadable binaries require a published GitHub Release. The agent runs locally; inference stays local only when you choose a local model endpoint.

## One-line install

```bash
curl -fsSL https://raw.githubusercontent.com/Metixpro/macron-agent/main/install.sh | bash
```

That one line installs the system packages (python3, git, ffmpeg, poppler, ssh, rsync…), the newest release checked against its SHA-256 sum, the hash-locked Python tools in a private venv, and the systemd service with lingering (it stays up after you close SSH); then `macron setup` asks, in order, for the bot token from @BotFather, your numeric Telegram id, your time zone (Asia/Tehran by default) and the provider and model, testing each for real. Running the same line again updates the program, keeps `~/.macron` and restarts the service. Flags: `bash -s -- --help` (e.g. `--yes` to ask nothing, `--no-tools`, `--no-service`). Later updates from Telegram: `/update`.

## Install from source

Build prerequisites: **Git, Make and Go 1.26+** ([Go downloads](https://go.dev/dl/)). Document/media helpers require **Python 3.12+** and FFmpeg. Validation was performed on Linux amd64 with Python 3.12.

```bash
git clone https://github.com/Metixpro/Macron-Agent.git
cd Macron-Agent
make build
./install.sh --binary ./bin/macron --no-setup
export PATH="$HOME/.local/bin:$PATH"
macron version
```

The binary installs into `~/.local/bin`, or `/usr/local/bin` as root. It builds without CGO and needs no database server.

To prepare additional tools from the checkout:

```bash
./install.sh --binary ./bin/macron --tools --browser-agent --no-setup
macron doctor --tools-only
```

`--tools` prepares system helpers and a private Python environment. `--browser` also downloads Chromium. `--browser-agent` adds the autonomous browser runtime in a separate environment. Python dependencies are installed from SHA-256 lockfiles. System package installation needs the appropriate privileges; native browser libraries, local voice weights and external service credentials are separate requirements. See [SERVER.md](SERVER.md).

## Run a task without Telegram

Use a tool-capable OpenAI-compatible model. Read credentials interactively rather than putting them into source or shell history:

```bash
read -r -p 'API URL (ending in /v1): ' MACRON_API_URL
read -r -s -p 'API key (empty for a local model): ' MACRON_API_KEY
printf '\n'
export MACRON_API_URL MACRON_API_KEY
export MACRON_MODEL=azm
macron exec --timeout 5m 'Create a Python disk-usage checker and test it'
unset MACRON_API_KEY
```

Choose a model name your endpoint supports. For an already-running Ollama instance with a downloaded tool-capable model:

```bash
export MACRON_API_URL=http://127.0.0.1:11434/v1
export MACRON_MODEL=qwen3:8b
macron exec 'Create a sample sales CSV, calculate totals and verify the output'
```

This does not install Ollama or download a model. Each task gets its own workspace, printed on stderr. The final answer goes to stdout; errors and partial results return a nonzero status. `exec` is currently one-shot: it does not inherit Telegram conversation archives, scheduling or provider-database failover.

The CLI starts with six shell/file tools and on-demand discovery. Override the surface when needed:

```bash
macron exec --tools bash_execute,read_file,write_file 'Inspect this server'
macron exec --all-tools 'Use the available tools for this task'
```

Tool selection controls capability and context size; shell access retains the account's privileges. It is not a sandbox.

## Telegram and service startup

```bash
macron setup
macron doctor
macron service install
macron service status
macron logs -f
```

Setup asks for a BotFather token, numeric owner ID, model endpoint/key/name and timezone. The bot answers configured owners in private chats only; it does not operate in groups or channels. Linux uses systemd; macOS uses launchd. Real systemd reboot recovery and macOS service operation were not validated in the cloud test instance.

## Capabilities

| Area | What it provides |
|---|---|
| Code and servers | Shell/code execution, project search, file writes and patches, tests, processes |
| Web | Search, page extraction, Chromium interaction, forms, screenshots and PDFs |
| Documents and data | PDF, CSV/JSON/SQLite, Excel, charts, QR and document/media helpers |
| Memory and skills | Editable SOUL/USER/MEMORY files and skills; learning in the Telegram workflow |
| Longer work | Subagents; background jobs and schedules in the Telegram workflow |
| Extensions | Plugin and MCP integration through supported application paths |

A registered tool does not establish that its external service is configured. See the [tool/dependency review](audits/TOOLS-AND-PACKAGES.md).

## Your state, in one folder

Default: `~/.macron`; override with `MACRON_HOME`.

| Path | Contents |
|---|---|
| `config.env`, `secret.key` | Private settings and secrets |
| `SOUL.md` | Editable personality; existing files are preserved |
| `USER.md`, `MEMORY.md` | Lasting owner facts and work notes |
| `macron.db` | Telegram conversations, search and application state |
| `skills/`, `workspaces/` | Procedures and task outputs |
| `venv/`, `browser-agent/`, `browsers/` | Optional private tool runtimes |

```bash
macron backup /private/backups/macron.tar.gz
macron backup --all /private/backups/macron-with-workspaces.tar.gz
```

Backups contain credentials. Stop the service before restoring; follow [the server guide](SERVER.md).

## Measured optimization

All three final `azm` pilot scenarios passed independent output checks. Aggregate reported input tokens decreased from **305,972** for the earlier broad-tool variant to **98,538**, approximately **68% in those samples**.

There was one trial per scenario/variant. Sampling, cache, network and upstream load were uncontrolled; tokens are not monetary cost. Hermes was also run against the same endpoint; this does **not** establish overall superiority. Raw attempts, evaluator corrections, infrastructure failures and limitations are retained in [the comparison report](benchmarks/RESULTS.md), [optimization report](audits/OPTIMIZATION.fa.md) and [validation notes](VALIDATION.md).

## Develop

```bash
make check
make build
go test -race ./pkg/agent ./pkg/cli ./pkg/config ./pkg/machine
```

Media/browser integration tests need optional dependencies; live-service tests may skip without credentials. For a development shell, prepend `~/.macron/venv/bin` to PATH after preparing tools.

`cmd/macron` contains the application; `pkg/agent` the loop; `pkg/tools` the tools; `pkg/llm` model clients; `pkg/brain` file memory; `pkg/store` SQLite; `pkg/cli` setup and maintenance.

## Before production

The agent runs with its account's privileges. Test with a dedicated account and recoverable data; root execution grants root access. Untrusted documents/web pages can mislead models, and tool filtering does not replace OS isolation. Run only trusted extensions.

Keep model keys, Telegram tokens, cookies and backups out of public repositories and issues. Native packages, paid media services and voice models may require additional setup.

---

By **Haj Meti** · [@Cianyd](https://t.me/Cianyd)
