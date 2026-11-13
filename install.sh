#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
 cat <<'USAGE'
Macron Agent · Installer

  curl -fsSL https://raw.githubusercontent.com/Metixpro/Macron-Agent/main/install.sh | bash

  --scope user|system  choose the installation owner before installing
  --home DIR           private data and Python environments
  --prefix DIR         program directory (user: ~/.local/bin; system: /usr/local/bin)
  --version vX.Y.Z      choose a release
  --binary FILE         install a local binary
  --from-source         build from GitHub (needs Go and Git)
  --source-dir DIR      build a local checkout
  --no-tools            skip Python dependencies and system-package installation
  --tools               install Python tools (default)
  --browser             add a private Chromium download
  --browser-agent       add the autonomous browser environment
  --no-service          do not run the Telegram bot in the background
  --no-setup            leave the model and Telegram for later
  --yes                 use defaults without questions
  --uninstall           run the complete macron uninstaller
  --keep-data           with --uninstall: preserve private data
  --help                show this help

User installations never invoke sudo for packages. Python packages live in a
private venv; missing Python/venv can be provisioned with a private uv runtime.
USAGE
}

have() { command -v "$1" >/dev/null 2>&1; }
say() { printf '  %s\n' "$*"; }
ok() { printf '  %s✓%s %s\n' "$GREEN" "$RESET" "$*"; }
warn() { printf '  %s!%s %s\n' "$YELLOW" "$RESET" "$*" >&2; }
die() { printf '\n  %s✗%s %s%s%s\n\n' "$RED" "$RESET" "$BOLD" "$*" "$RESET" >&2; exit 1; }
seconds() {
 local s="$1"
 if [ "$s" -lt 60 ]; then printf '%ss' "$s"; else printf '%sm %02ss' "$((s / 60))" "$((s % 60))"; fi
}
doing() { CURRENT="$*"; }
banner() {
 local rows=(
  '█   █  ███   ████ ████   ███  █   █'
  '██ ██ █   █ █     █   █ █   █ ██  █'
  '█ █ █ █████ █     ████  █   █ █ █ █'
  '█   █ █   █ █     █ █   █   █ █  ██'
  '█   █ █   █  ████ █  ██  ███  █   █'
 ) i
 printf '\n'
 for i in 0 1 2 3 4; do
  if [ "$i" -lt 3 ]; then printf '  %s%s%s\n' "$BOLD" "${rows[$i]}" "$RESET"; else printf '  %s%s%s%s\n' "$BOLD" "$ACCENT" "${rows[$i]}" "$RESET"; fi
 done
 printf '\n  %sYour machine. Your agent.%s   %sinstaller · %s/%s%s\n' "$BOLD" "$RESET" "$DIM" "$OS" "$ARCH" "$RESET"
}
phase() {
 local i bar=''
 for i in 1 2 3 4 5 6; do if [ "$i" -le "$1" ]; then bar="$bar▰"; else bar="$bar▱"; fi; done
 printf '\n  %s◆%s %s%s%s  %s%s  step %s of 6%s\n' "$ACCENT" "$RESET" "$BOLD" "$2" "$RESET" "$ACCENT" "$bar" "$1" "$RESET"
}
panel() {
 local title="$1"; shift
 printf '  %s╭─%s %s%s%s\n' "$DIM" "$RESET" "$BOLD" "$title" "$RESET"
 while [ $# -gt 1 ]; do
  printf '  %s│%s  %s%-8s%s %s\n' "$DIM" "$RESET" "$DIM" "$1" "$RESET" "$2"
  shift 2
 done
 printf '  %s╰─%s\n' "$DIM" "$RESET"
}
ask() {
 local reply="" shown="$2"
 case "$2" in Y) shown='Y/n' ;; N) shown='y/N' ;; esac
 printf '  %s?%s %s%s%s %s[%s]%s ' "$ACCENT" "$RESET" "$BOLD" "$1" "$RESET" "$DIM" "$shown" "$RESET" >/dev/tty
 IFS= read -r reply </dev/tty || reply=""
 ANSWER="${reply:-$2}"
}

cleanup() {
 if [ -n "${INSTALL_JOB:-}" ] && kill -0 "$INSTALL_JOB" 2>/dev/null; then kill "$INSTALL_JOB" 2>/dev/null || true; fi
 [ -z "${STAGE:-}" ] || rm -rf -- "$STAGE"
 [ -z "${TMP:-}" ] || rm -rf -- "$TMP"
}

run_logged() {
 local label="${CURRENT:-Working}" started=$SECONDS
 CURRENT=''
 "$@" >>"$LOG" 2>&1 &
 INSTALL_JOB=$!
 if [ -t 1 ] && [ "${TERM:-dumb}" != dumb ]; then
  local marks='⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏' index=0
  while kill -0 "$INSTALL_JOB" 2>/dev/null; do
   printf '\r\033[K  %s%s%s %s %s%s%s' "$ACCENT" "${marks:index%10:1}" "$RESET" "$label" "$DIM" "$(seconds $((SECONDS - started)))" "$RESET"
   index=$((index+1)); sleep 0.1
  done
  printf '\r\033[K'
 fi
 if wait "$INSTALL_JOB"; then
  INSTALL_JOB=""
  [ "$label" = Working ] || printf '  %s✓%s %s %s%s%s\n' "$GREEN" "$RESET" "$label" "$DIM" "$(seconds $((SECONDS - started)))" "$RESET"
  return 0
 fi
 INSTALL_JOB=""
 printf '  %s✗%s %s\n' "$RED" "$RESET" "$label" >&2
 tail -n 12 "$LOG" | sed 's/^/    /' >&2 || true
 warn "Full log: $LOG"
 return 1
}

validate_home() {
 case "$HOME_DIR$PREFIX" in *$'\n'*|*$'\r'*) die "installation paths cannot contain line breaks" ;; esac
 [ "${HOME_DIR#/}" != "$HOME_DIR" ] || die "--home must be an absolute path"
 local normalized='' part ancestor suffix='' resolved
 local parts=()
 IFS=/ read -r -a parts <<<"$HOME_DIR"
 for part in "${parts[@]}"; do
  case "$part" in ''|.) ;; ..) normalized="${normalized%/*}" ;; *) normalized="$normalized/$part" ;; esac
 done
 HOME_DIR="${normalized:-/}"
 [ ! -L "$HOME_DIR" ] || die "--home must name the real directory, not a symlink"
 ancestor="$HOME_DIR"
 while [ ! -e "$ancestor" ]; do
  suffix="/${ancestor##*/}$suffix"; ancestor="${ancestor%/*}"; ancestor="${ancestor:-/}"
 done
 resolved="$(cd "$ancestor" && pwd -P)" || die "--home must name a directory"
 HOME_DIR="${resolved%/}$suffix"; HOME_DIR="${HOME_DIR:-/}"
 case "$HOME_DIR" in /|"$HOME"|/tmp|/var|/var/lib|/etc|/usr|/usr/local|/home|/root|/workspace|/opt) die "choose a dedicated Macron data directory" ;; esac
 [ ! -e "$HOME_DIR/.git" ] || die "a source checkout cannot be the data directory"
 if [ -d "$HOME_DIR" ] && [ -n "$(ls -A "$HOME_DIR" 2>/dev/null)" ] && [ ! -f "$HOME_DIR/config.env" ] && [ ! -f "$HOME_DIR/macron.db" ] && [ ! -f "$HOME_DIR/.install" ] && [ ! -f "$HOME_DIR/.macron-home" ] && [ "${HOME_DIR##*/}" != .macron ]; then
  die "$HOME_DIR is not an existing Macron home; choose an empty directory"
 fi
}

write_record() {
 local record
 record="$(mktemp "$PREFIX/.macron-record.XXXXXX")"
 printf 'macron-install-v1\nbinary=%s\nhome=%s\nscope=%s\nuid=%s\n' "$TARGET" "$HOME_DIR" "$INSTALL_SCOPE" "$(id -u)" >"$record"
 chmod 0644 "$record"
 mv -f "$record" "$PREFIX/.macron-install"
 cp "$PREFIX/.macron-install" "$HOME_DIR/.install"
 chmod 0600 "$HOME_DIR/.install"
}

install_path() {
 case ":$PATH:" in *":$PREFIX:"*) return ;; esac
 case "${SHELL:-}" in */fish) warn "Add $PREFIX to your fish PATH"; return ;; esac
 local rc="$HOME/.profile" quoted
 [ "${SHELL##*/}" != zsh ] || rc="$HOME/.zshrc"
 [ "${SHELL##*/}" != bash ] || rc="$HOME/.bashrc"
 quoted="'${PREFIX//\'/\'\\\'\'}'"
 local path_line="export PATH=$quoted:\"\$PATH\""
 if ! grep -Fxq "$path_line" "$rc" 2>/dev/null; then
  if ! printf '\n# >>> macron PATH >>>\n%s\n# <<< macron PATH <<<\n' "$path_line" 2>/dev/null >>"$rc"; then
   warn "Could not write $rc; add $PREFIX to your PATH yourself"
   return
  fi
  ok "PATH configured in $rc"
  PATH_RC="$rc"
 fi
}

bootstrap_uv() {
 if have uv; then UV="$(command -v uv)"; return; fi
 UV="$HOME_DIR/runtime/uv/uv"
 [ ! -x "$UV" ] || return
 local triple archive expected actual
 case "$OS/$ARCH" in
  linux/amd64) triple=x86_64-unknown-linux-gnu ;;
  linux/arm64) triple=aarch64-unknown-linux-gnu ;;
  darwin/amd64) triple=x86_64-apple-darwin ;;
  darwin/arm64) triple=aarch64-apple-darwin ;;
 esac
 if [ "$OS" = linux ] && have ldd && ldd --version 2>&1 | grep -qi musl; then triple="${triple%-gnu}-musl"; fi
 archive="uv-$triple.tar.gz"
 say 'Preparing a private Python installer…'
 fetch "https://github.com/astral-sh/uv/releases/download/0.13.5/$archive" "$TMP/$archive" || die "could not download uv"
 case "$triple" in
  x86_64-unknown-linux-gnu) expected=f68d291c75783122dad58ab77def0e13f3233f358731db5d6d7e8715603b928f ;;
  aarch64-unknown-linux-gnu) expected=4bb69cf95ed13f23713bb7bc09f241896ea0c75b7c557e615a20604b82f9c403 ;;
  x86_64-unknown-linux-musl) expected=57ef9d9ccb01fb1bd998df995b0dd221b52f7d7841fbea93f2d4463bf61fb7a9 ;;
  aarch64-unknown-linux-musl) expected=7906c79adb21c81abd559841ba76bb1d9af9d791acac7a041b2972e03ae16538 ;;
  x86_64-apple-darwin) expected=b0f52b0e3945b4107d89024ea38e625683011fbfc05a0d98f9fa2d6dcee28c66 ;;
  aarch64-apple-darwin) expected=743f85035a1996ecb350659e01cd9faea9709829be09b1b4b03c73eaf3c9cd73 ;;
 esac
 actual="$(sha256_of "$TMP/$archive")"
 [ "$expected" = "$actual" ] || die "uv checksum mismatch"
 mkdir -p "$HOME_DIR/runtime/uv" "$TMP/uv"
 tar -xzf "$TMP/$archive" -C "$TMP/uv"
 install -m 0755 "$TMP/uv/uv-$triple/uv" "$UV"
}

python_ok() { "$1" -c 'import sys; sys.exit(0 if sys.version_info >= (3,12) else 1)' 2>/dev/null; }

prepare_python() {
 PY=""
 for candidate in python3 python3.13 python3.12; do if have "$candidate" && python_ok "$candidate"; then PY="$(command -v "$candidate")"; break; fi; done
 export UV_PYTHON_INSTALL_DIR="$HOME_DIR/python" UV_CACHE_DIR="$HOME_DIR/cache/uv" PIP_CACHE_DIR="$HOME_DIR/cache/pip"
 if [ -z "$PY" ]; then
  bootstrap_uv
  doing 'Private Python 3.12'
  run_logged "$UV" python install 3.12 || die "could not provision private Python 3.12"
  PY="$("$UV" python find --managed-python 3.12)"
 fi
 doing 'Python environment'
 if ! run_logged "$PY" -m venv "$HOME_DIR/venv"; then
  bootstrap_uv
  doing 'Python environment (with uv)'
  run_logged "$UV" venv --seed --python "$PY" "$HOME_DIR/venv" || die "could not create the private Python environment"
 fi
 PY="$HOME_DIR/venv/bin/python"
}

install_system_packages() {
 local pkgs='python3 python3-pip python3-venv git curl ffmpeg jq unzip poppler-utils openssh-client sshpass rsync'
 if [ "$INSTALL_SCOPE" = user ]; then
  say 'User scope · system packages are left as they are'
 elif have apt-get; then
  doing 'Package lists'
  run_logged apt-get update -qq || die 'could not refresh system packages'
  doing 'Python, Git, FFmpeg, Poppler and SSH tools'
  run_logged env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq $pkgs || die 'system packages did not install'
  doing 'GitHub CLI'
  run_logged env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq gh || warn 'GitHub CLI is unavailable'
  doing '7-Zip'
  run_logged env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq p7zip-full || { doing '7-Zip'; run_logged env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq 7zip; } || warn '7-Zip is unavailable (password, .7z and .rar archives)'
 elif have dnf; then
  doing 'Python, Git, FFmpeg, Poppler and SSH tools'
  run_logged dnf install -y -q python3 python3-pip git curl ffmpeg jq unzip poppler-utils openssh-clients sshpass rsync gh || die 'system packages did not install'
  doing '7-Zip'
  run_logged dnf install -y -q p7zip p7zip-plugins || warn '7-Zip is unavailable (password, .7z and .rar archives)'
 elif have brew; then
  warn 'Use user scope with Homebrew; root does not own Homebrew packages.'
 else
  warn 'No supported system package manager; existing binaries will be used.'
 fi
}

install_python_packages() {
 local req="$SCRIPT_DIR/requirements-tools.lock"
 [ -z "$SOURCE_DIR" ] || req="$SOURCE_DIR/requirements-tools.lock"
 if [ ! -f "$req" ] && [ -n "$RELEASE_DIR" ] && [ -f "$RELEASE_DIR/requirements-tools.lock" ]; then req="$RELEASE_DIR/requirements-tools.lock"; fi
 if [ ! -f "$req" ]; then req="$TMP/requirements-tools.lock"; fetch "https://raw.githubusercontent.com/$REPO/${VERSION:-${RELEASE_TAG:-main}}/requirements-tools.lock" "$req" || die 'could not download the Python lockfile'; fi
 prepare_python
 doing 'Python tools (documents, media, data, web)'
 run_logged "$PY" -m pip install --disable-pip-version-check --require-hashes -r "$req" || die 'Python tools did not install; rerun to finish'
 if [ "$BROWSER_AGENT" = 1 ]; then
  local agent_req="$SCRIPT_DIR/requirements-browser-agent.lock"
  [ -z "$SOURCE_DIR" ] || agent_req="$SOURCE_DIR/requirements-browser-agent.lock"
  if [ ! -f "$agent_req" ] && [ -n "$RELEASE_DIR" ] && [ -f "$RELEASE_DIR/requirements-browser-agent.lock" ]; then agent_req="$RELEASE_DIR/requirements-browser-agent.lock"; fi
  if [ ! -f "$agent_req" ]; then agent_req="$TMP/requirements-browser-agent.lock"; fetch "https://raw.githubusercontent.com/$REPO/${VERSION:-${RELEASE_TAG:-main}}/requirements-browser-agent.lock" "$agent_req" || die 'could not download browser requirements'; fi
  doing 'Browser-agent environment'
  run_logged "$PY" -m venv "$HOME_DIR/browser-agent" || die 'could not create browser-agent environment'
  doing 'Browser-agent packages'
  run_logged "$HOME_DIR/browser-agent/bin/python" -m pip install --disable-pip-version-check --require-hashes -r "$agent_req" || die 'browser-agent dependencies did not install'
 fi
 if [ "$BROWSER" = 1 ]; then
  doing 'Private Chromium'
  run_logged env PLAYWRIGHT_BROWSERS_PATH="$HOME_DIR/browsers" "$PY" -m playwright install chromium || die 'Chromium did not install'
 fi
 ok "Python tools ready ${DIM}$HOME_DIR/venv${RESET}"
}

report_dependencies() {
 local missing='' command
 for command in git ffmpeg jq unzip ssh rsync; do if ! have "$command"; then missing="$missing $command"; fi; done
 if [ -n "$missing" ]; then warn "Optional system binaries missing:$missing"; warn 'Python packages are installed privately; the capabilities needing these binaries need them separately.'; fi
}

start_service() {
 [ "$SERVICE" = 1 ] || return 0
 if { [ "$OS" = darwin ] || have systemctl; } && "$TARGET" service install; then ok 'Telegram bot service configured'; return 0; fi
 if "$TARGET" telegram start; then ok 'Telegram bot running in the background'; else warn 'The Telegram bot did not start; see: macron telegram logs'; fi
}

choose_scope() {
 if [ -z "$INSTALL_SCOPE" ]; then
  if [ "$(id -u)" = 0 ]; then INSTALL_SCOPE=system; else INSTALL_SCOPE=user; fi
  if [ "$INTERACTIVE" = 1 ]; then
   printf '\n  %s?%s %sWhere should Macron be installed?%s\n' "$ACCENT" "$RESET" "$BOLD" "$RESET"
   printf '    %s1%s  User     %scurrent account · private Python · no sudo%s\n' "$ACCENT" "$RESET" "$DIM" "$RESET"
   printf '    %s2%s  System   %s/usr/local/bin and a system service · needs root%s\n' "$ACCENT" "$RESET" "$DIM" "$RESET"
   local default=1; [ "$(id -u)" != 0 ] || default=2
   ask 'Choose' "$default"
   case "$ANSWER" in 1|user) INSTALL_SCOPE=user ;; 2|system) INSTALL_SCOPE=system ;; *) die 'choose 1 or 2' ;; esac
  fi
 fi
 case "$INSTALL_SCOPE" in user|system) ;; *) die '--scope must be user or system' ;; esac
 if [ -z "$PREFIX" ]; then if [ "$INSTALL_SCOPE" = system ]; then PREFIX=/usr/local/bin; else PREFIX="$HOME/.local/bin"; fi; fi
 if [ -z "$HOME_DIR" ]; then
  if [ "$INSTALL_SCOPE" = system ] && [ "$OS" = linux ]; then HOME_DIR=/var/lib/macron; else HOME_DIR="$HOME/.macron"; fi
 fi
}

main() {
 REPO="${MACRON_REPO:-Metixpro/Macron-Agent}"
 VERSION='' RELEASE_TAG='' RELEASE_DIR='' PREFIX='' BINARY='' SOURCE_DIR='' INSTALL_SCOPE=''
 HOME_DIR="${MACRON_HOME:-}" SCRIPT_DIR=''
 if [ -n "${BASH_SOURCE[0]:-}" ] && [ -f "${BASH_SOURCE[0]}" ]; then SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"; fi
 FROM_SOURCE=0 BROWSER_AGENT=0 BROWSER=0 TOOLS=1 SERVICE=1 ASSUME_YES=0 SETUP=1 UNINSTALL=0 KEEP_DATA=0 INTERACTIVE=0
 GREEN='' YELLOW='' RED='' CYAN='' RESET='' ACCENT='' BOLD='' DIM='' TMP='' STAGE='' INSTALL_JOB='' CURRENT='' PATH_RC=''
 local original=("$@")
 while [ $# -gt 0 ]; do
  case "$1" in
   --scope) INSTALL_SCOPE="${2:?--scope needs user or system}"; shift ;;
   --home) HOME_DIR="${2:?--home needs a directory}"; shift ;;
   --prefix) PREFIX="${2:?--prefix needs a directory}"; shift ;;
   --version) VERSION="${2:?--version needs a value}"; shift ;;
   --binary) BINARY="${2:?--binary needs a file}"; shift ;;
   --from-source) FROM_SOURCE=1 ;;
   --source-dir) SOURCE_DIR="${2:?--source-dir needs a directory}"; FROM_SOURCE=1; shift ;;
   --no-tools) TOOLS=0 ;;
   --tools) TOOLS=1 ;;
   --browser) BROWSER=1 ;;
   --browser-agent) BROWSER_AGENT=1; BROWSER=1 ;;
   --no-service) SERVICE=0 ;;
   --no-setup) SETUP=0 ;;
   --yes|-y) ASSUME_YES=1 ;;
   --uninstall) UNINSTALL=1 ;;
   --keep-data) KEEP_DATA=1 ;;
   --help|-h) usage; return ;;
   *) die "unknown option: $1 (see --help)" ;;
  esac
  shift
 done
 if [ -t 1 ] && [ "${TERM:-dumb}" != dumb ] && [ -z "${NO_COLOR:-}" ]; then
  GREEN=$'\033[32m'; YELLOW=$'\033[33m'; RED=$'\033[31m'; CYAN=$'\033[36m'; RESET=$'\033[0m'; BOLD=$'\033[1m'; DIM=$'\033[2m'
  ACCENT=$'\033[38;5;111m'
  case "${COLORTERM:-}" in truecolor|24bit) ACCENT=$'\033[38;2;120;170;255m'; GREEN=$'\033[38;2;63;185;80m'; YELLOW=$'\033[38;2;210;153;34m'; RED=$'\033[38;2;248;81;73m' ;; esac
 fi
 if [ "$ASSUME_YES" = 0 ] && [ -r /dev/tty ] && { : </dev/tty; } 2>/dev/null; then INTERACTIVE=1; fi
 OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
 case "$(uname -m)" in x86_64|amd64) ARCH=amd64 ;; aarch64|arm64) ARCH=arm64 ;; *) ARCH="$(uname -m)" ;; esac
 [ -n "${MACRON_INSTALL_CHILD:-}" ] || banner
 case "$OS" in linux|darwin) ;; *) die 'Linux and macOS are supported; use WSL2 on Windows' ;; esac
 case "$ARCH" in amd64|arm64) ;; *) die "unsupported CPU architecture: $ARCH" ;; esac
 choose_scope
 if [ "$INSTALL_SCOPE" = system ] && [ "$(id -u)" != 0 ]; then
  have sudo || die 'system scope needs root/sudo; choose --scope user for a private installation'
  local bootstrap_dir bootstrap
  bootstrap_dir="$(mktemp -d)"; bootstrap="$bootstrap_dir/install.sh"
  printf '#!/usr/bin/env bash\nset -Eeuo pipefail\n' >"$bootstrap"
  declare -f usage have say ok warn die seconds doing banner phase panel ask cleanup run_logged validate_home write_record install_path bootstrap_uv python_ok prepare_python install_system_packages install_python_packages report_dependencies start_service choose_scope fetch sha256_of build_from_source fetch_release_asset download_release main >>"$bootstrap"
  printf '\nmain "$@"\n' >>"$bootstrap"
  chmod 0600 "$bootstrap"
  local code=0
  local sudo_args=(-H)
  [ "$ASSUME_YES" = 0 ] || sudo_args=(-n -H)
  sudo "${sudo_args[@]}" env MACRON_INSTALL_CHILD=1 bash "$bootstrap" "${original[@]}" --scope system --home "$HOME_DIR" || code=$?
  rm -rf -- "$bootstrap_dir"
  return "$code"
 fi
 TARGET="$PREFIX/macron"
 if [ "$UNINSTALL" = 1 ]; then
  [ -x "$TARGET" ] || die "no Macron binary at $TARGET"
  local remove_args=(); [ "$ASSUME_YES" = 0 ] || remove_args+=(--yes); [ "$KEEP_DATA" = 0 ] || remove_args+=(--keep-data)
  if [ "$INTERACTIVE" = 1 ]; then "$TARGET" uninstall "${remove_args[@]}" </dev/tty; else "$TARGET" uninstall "${remove_args[@]}"; fi
  return
 fi
 [ "$TOOLS" = 1 ] || { [ "$BROWSER" = 0 ] || die '--browser needs Python tools; remove --no-tools'; }
 validate_home
 INSTALL_STARTED=$SECONDS
 phase 1 'Plan'
 local what='Python tools, system packages'
 [ "$TOOLS" = 1 ] || what='program only (--no-tools)'
 [ "$INSTALL_SCOPE" = system ] || [ "$TOOLS" = 0 ] || what='Python tools (private, no sudo)'
 [ "$BROWSER" = 0 ] || what="$what, Chromium"
 panel 'Macron Agent will be installed' Account "$(id -un)" Scope "$INSTALL_SCOPE" Program "$TARGET" Data "$HOME_DIR" Extras "$what"
 if [ "$INTERACTIVE" = 1 ]; then ask 'Continue?' Y; case "$ANSWER" in Y|y|yes|YES) ;; *) say 'Cancelled; nothing was changed.'; return ;; esac; fi
 TMP="$(mktemp -d)"; trap cleanup EXIT; trap 'exit 130' INT TERM
 mkdir -p "$HOME_DIR"; chmod 0700 "$HOME_DIR"
 printf 'macron-home-v1\n' >"$HOME_DIR/.macron-home"
 LOG="$HOME_DIR/install.log"
 export MACRON_HOME="$HOME_DIR"
 phase 2 'Program'
 if [ -n "$BINARY" ]; then [ -f "$BINARY" ] || die "no such binary: $BINARY"; cp "$BINARY" "$TMP/macron"
 elif [ "$FROM_SOURCE" = 1 ]; then build_from_source
 elif ! download_release; then warn 'No release available; trying a source build'; build_from_source
 fi
 mkdir -p "$PREFIX"; PREFIX="$(cd "$PREFIX" && pwd)"; TARGET="$PREFIX/macron"
 STAGE="$(mktemp -d "$PREFIX/.macron-install.XXXXXX")"
 install -m 0755 "$TMP/macron" "$STAGE/macron"
 "$STAGE/macron" version >/dev/null || die 'the new program does not start; the installed program was kept'
 local old_version='' new_version
 if [ -x "$TARGET" ]; then old_version="$("$TARGET" version 2>/dev/null | awk '{print $NF}' || true)"; fi
 if [ -e "$TARGET" ]; then cp -p "$TARGET" "$STAGE/macron.old"; mv -f "$STAGE/macron.old" "$TARGET.old"; fi
 mv -f "$STAGE/macron" "$TARGET"
 new_version="$("$TARGET" version | awk '{print $NF}')"
 write_record; install_path
 if [ -n "$old_version" ] && [ "$old_version" != "$new_version" ]; then ok "Updated ${BOLD}$old_version → $new_version${RESET}"; else ok "Macron ${BOLD}$new_version${RESET} is in place ${DIM}$TARGET${RESET}"; fi
 phase 3 'System packages'
 if [ "$TOOLS" = 1 ]; then install_system_packages; else say 'Skipped (--no-tools)'; fi
 phase 4 'Python tools'
 if [ "$TOOLS" = 1 ]; then install_python_packages; report_dependencies; else say 'Skipped (--no-tools)'; fi
 phase 5 'Model'
 if [ -f "$HOME_DIR/config.env" ]; then ok "Your settings in $HOME_DIR are kept"
 elif [ "$SETUP" = 1 ] && [ "$INTERACTIVE" = 1 ]; then MACRON_INSTALLER=1 "$TARGET" setup --no-telegram </dev/tty || warn 'Choose it later: macron setup'
 else say 'Later: Macron asks for it the first time it runs (or: macron setup)'; fi
 phase 6 'Telegram'
 local telegram=0
 if [ -f "$HOME_DIR/config.env" ] && grep -q '^TELEGRAM_BOT_TOKEN=..*' "$HOME_DIR/config.env"; then
  telegram=1
  if [ "$SERVICE" = 1 ]; then start_service; else say 'Connected; start it with: macron telegram start'; fi
 elif [ "$SETUP" = 1 ] && [ "$INTERACTIVE" = 1 ]; then
  ask 'Connect a Telegram bot too, to reach Macron from your phone?' N
  case "$ANSWER" in
   y|Y|yes|YES)
    if [ "$SERVICE" = 1 ]; then "$TARGET" telegram setup </dev/tty && telegram=1; else "$TARGET" telegram setup --no-service </dev/tty && telegram=1; fi ;;
   *) say "Later: $([ -n "$PATH_RC" ] && echo macron || echo "$TARGET") telegram" ;;
  esac
 else say 'Later: macron telegram'; fi
 local run="$TARGET"
 if [ "$(command -v macron 2>/dev/null || true)" = "$TARGET" ] || [ -n "$PATH_RC" ]; then run=macron; fi
 printf '\n  %s%s✓ Macron Agent %s is ready%s  %sin %s%s\n\n' "$GREEN" "$BOLD" "$new_version" "$RESET" "$DIM" "$(seconds $((SECONDS - INSTALL_STARTED)))" "$RESET"
 panel 'Installed' Program "$TARGET" Data "$HOME_DIR" Log "$LOG"
 printf '\n  %sNext%s\n' "$BOLD" "$RESET"
 printf '  %s›%s %-30s %sopen the terminal workspace in a project%s\n' "$ACCENT" "$RESET" "cd your-project && $run" "$DIM" "$RESET"
 if [ "$telegram" = 0 ]; then printf '  %s›%s %-30s %sreach Macron from your phone%s\n' "$ACCENT" "$RESET" "$run telegram" "$DIM" "$RESET"; fi
 printf '  %s›%s %-30s %scheck everything works%s\n' "$ACCENT" "$RESET" "$run doctor" "$DIM" "$RESET"
 printf '  %s›%s %-30s %sremove it again%s\n' "$ACCENT" "$RESET" "$run uninstall" "$DIM" "$RESET"
 if [ -n "$PATH_RC" ]; then printf '\n  %sOpen a new terminal first (or run: source %s) so «macron» is found.%s\n' "$DIM" "$PATH_RC" "$RESET"; fi
 printf '\n'
}

fetch() {
	if have curl; then curl -fsSL --retry 3 -o "$2" "$1"; elif have wget; then wget -q -O "$2" "$1"; else die "curl or wget is needed"; fi
}

sha256_of() {
	if have sha256sum; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}


build_from_source() {
	[ -n "$SOURCE_DIR" ] || have git || die "--from-source needs git"
	have go  || die "--from-source needs Go (https://go.dev/dl/). Any Go 1.21+ will do: it fetches the toolchain the project asks for."
	say "Building from source (a few minutes the first time)…"
	local src="$SOURCE_DIR"
 if [ -n "$src" ]; then
  [ -f "$src/go.mod" ] && [ -d "$src/cmd/macron" ] || die "not a Macron source folder: $src"
  src="$(cd "$src" && pwd)"
 else
  git clone --depth 1 ${VERSION:+--branch "$VERSION"} "https://github.com/$REPO.git" "$TMP/src" >/dev/null 2>&1 || die "cannot clone $REPO"
  src="$TMP/src"
 fi
 (cd "$src" && CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags "-s -w" -o "$TMP/macron" ./cmd/macron) || die "the build failed"
}


fetch_release_asset() {
    fetch "$1" "$3" 2>/dev/null && return 0
    have python3 || return 1
    [ -f "$TMP/release.json" ] || fetch "$4/tags/$5" "$TMP/release.json" 2>/dev/null || return 1
    local asset_api
    asset_api="$(python3 - "$TMP/release.json" "$2" "$REPO" <<'PYCODE'
import json, sys
release = json.load(open(sys.argv[1]))
for asset in release.get("assets", []):
    url = asset.get("url", "")
    prefix = "https://api.github.com/repos/" + sys.argv[3] + "/releases/assets/"
    if asset.get("name") == sys.argv[2] and url.lower().startswith(prefix.lower()):
        print(url)
        break
PYCODE
)" || return 1
    [ -n "$asset_api" ] || return 1
    if have curl; then
        curl -fsSL --retry 3 -H 'Accept: application/octet-stream' -o "$3" "$asset_api"
    elif have wget; then
        wget -q --header='Accept: application/octet-stream' -O "$3" "$asset_api"
    else
        return 1
    fi
}


download_release() {
	local tag="$VERSION" api="https://api.github.com/repos/$REPO/releases"
	if [ -z "$tag" ]; then
		fetch "$api/latest" "$TMP/release.json" 2>/dev/null || return 1
		tag="$(sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' "$TMP/release.json" | head -n1)"
		[ -n "$tag" ] || return 1
	fi
	RELEASE_TAG="$tag"
	local ver="${tag#v}" name base
	name="macron-$ver-$OS-$ARCH.tar.gz"
	base="https://github.com/$REPO/releases/download/$tag"
	say "Downloading Macron Agent ${BOLD}$ver${RESET} for $OS/$ARCH…"
	fetch_release_asset "$base/$name" "$name" "$TMP/$name" "$api" "$tag" || return 1
	fetch_release_asset "$base/SHA256SUMS" "SHA256SUMS" "$TMP/SHA256SUMS" "$api" "$tag" || return 1
	local want got
	want="$(grep " $name\$" "$TMP/SHA256SUMS" | head -n1 | cut -d' ' -f1)"
	got="$(sha256_of "$TMP/$name")"
	[ -n "$want" ] && [ "$want" = "$got" ] || die "the download does not match its SHA-256 sum; nothing was installed"
	tar -xzf "$TMP/$name" -C "$TMP"
	cp "$TMP/macron-$ver-$OS-$ARCH/macron" "$TMP/macron"
	RELEASE_DIR="$TMP/macron-$ver-$OS-$ARCH"
	ok "Download verified (SHA-256)"
}


main "$@"
