# Tool and dependency review

The runtime inventories in macron-tools.json and hermes-tools.json were read
from both actual registries (Hermes ee6db222). They contain 40 and 73 registrations
in the inspected configurations, respectively. This is not a capability ranking:
Macron adds memory/scheduling/Telegram tools at the caller; Hermes loads further
plugins and service-dependent tools. Credentials were absent for paid tool APIs.

| Capability | Hermes | Macron and verified action |
|---|---|---|
| Shell, processes, files, patches and search | Terminal/file toolsets | Existing tools retained; small default CLI surface plus lazy discovery; restricted prompts no longer instruct absent tools |
| Tool discovery and token footprint | Narrow core plus deferred plugin schemas | CLI starts with six shell/file schemas and more_tools; compact catalog exposes other names; regression executes a hidden tool after discovery |
| Prompt/cache | Conversation prefix invariants | Task/clock kept outside system prefix; discovery does not rebuild system text; repeated default persona and CLI guidance shortened |
| Skills | On-demand skill bodies | Existing on-demand read_skill retained; custom SOUL remains supported and existing home files are not overwritten |
| Browser | browser harness/CDP and optional backends | Playwright/Chromium installed; real fill/click/DOM/screenshot and existing Go browser integration tests enabled; standard user browser cache detection repaired |
| Autonomous browser | Browser tools/subagents | browser-use prepared separately; runtime imports checked and real local-site autonomous navigation passed against a scripted model; live-provider autonomous navigation remains unrun |
| PDF, charts, Persian text | Skills/tool dependencies | PyMuPDF, arabic-reshaper and python-bidi added and pinned; previously unavailable PDF/RTL paths exercised; PDF smoke assertion now tolerates extraction whitespace |
| Downloads and speech | Optional integrations | yt-dlp, gallery-dl, edge-tts and piper-tts added; local voice weights and paid endpoints still require separate setup |
| Health checks | Doctor and PM tooling | doctor --tools-only checks actual Python imports without Telegram/model credentials; missing optional OS programs remain explicit warnings |
| Long-term state, jobs, plugins/MCP | Broad session and extension lifecycle | Macron's existing Telegram/session features reviewed; CLI remains one-shot and does not acquire session archive/scheduler parity in this change |

Installation:

- `install.sh --tools`: pinned private Python tool environment, including PDF,
  RTL, document, media and Playwright packages.
- `install.sh --browser`: also downloads Chromium in MACRON_HOME/browsers.
- `install.sh --browser-agent`: additionally prepares browser-use in its own
  MACRON_HOME/browser-agent environment, using the SHA-256-locked requirements-browser-agent.lock.
- The runtime discovers these private paths without overwriting explicit
  PLAYWRIGHT_BROWSERS_PATH/WAZE_BROWSER_USE_PYTHON settings.
- Native browser libraries must exist on the host. On supported hosts use
  the official Playwright `install-deps chromium` command with the host's
  package-manager privileges. The current cloud host already supported launch.

The current host has no sudo or scoped package installer. Optional sshpass and
rsync remain absent here; the existing --tools installer requests them through
APT on a normal Debian/Ubuntu server. SSH itself is present. Voice weights,
paid media credentials, browser login credentials, production SSH targets and
live messaging tests are not supplied by installing a Python package.

The first smaller-prompt/heuristic-focus experiment passed all three AZM tasks
but used 223,484 input tokens in total, more than the previous manual-six-tools
variant. It is preserved in azm-optimized.json and is not presented as a win.
The final small-core default is measured separately in azm-efficient.json.
A lower initial payload alone does not guarantee fewer total tokens or calls.

Both profiles were rebuilt in clean environments, checked for dependency consistency, and scanned with pip-audit. Go x/net was patched to 0.62.0 after a reachable govulncheck finding. Lockfiles are installed with --require-hashes.
