#!/usr/bin/env bash
# Macron Agent installer (Linux and macOS).
#
#   curl -fsSL https://raw.githubusercontent.com/Metixpro/macron-agent/main/install.sh | bash
#
# It downloads the newest release for this machine (checking its SHA-256 sum),
# puts the `macron` program on your PATH and starts `macron setup`, which asks for
# the bot token from @BotFather, your Telegram id and a model. Running it again
# updates the program and keeps your settings.
#
# Options:
#   --version vX.Y.Z   install that release instead of the newest
#   --prefix DIR       put the program in DIR (default: /usr/local/bin as root, else ~/.local/bin)
#   --binary FILE      install this program instead of downloading one
#   --from-source      build it here (needs git and Go) instead of downloading a release
#   --source-dir DIR   build a local source checkout (no GitHub clone)
#   --browser        also download Chromium into the private home
#   --browser-agent  also install the autonomous browser runtime in a separate venv
#   --tools            also install the tools the agent likes to have: python3, git, curl, ffmpeg, ssh, sshpass…
#   --no-setup         only install the program
#   --uninstall        remove the service and the program (your data in ~/.macron stays)
#   --purge            with --uninstall: remove ~/.macron too, after asking
set -Eeuo pipefail

REPO="${MACRON_REPO:-Metixpro/macron-agent}"
VERSION=""
PREFIX=""
BINARY=""
FROM_SOURCE=0
SOURCE_DIR=""
SCRIPT_DIR=""
if [ -n "${BASH_SOURCE[0]:-}" ]; then SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"; fi
BROWSER_AGENT=0
BROWSER=0
TOOLS=0
SETUP=1
UNINSTALL=0
PURGE=0

say()  { printf '%s\n' "$*"; }
ok()   { printf '✅ %s\n' "$*"; }
warn() { printf '⚠️  %s\n' "$*" >&2; }
die()  { printf '❌ %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

while [ $# -gt 0 ]; do
	case "$1" in
	--version)     VERSION="${2:?--version needs a value}"; shift ;;
	--prefix)      PREFIX="${2:?--prefix needs a folder}"; shift ;;
	--binary)      BINARY="${2:?--binary needs a file}"; shift ;;
	--from-source) FROM_SOURCE=1 ;;
 --source-dir) SOURCE_DIR="${2:?--source-dir needs a folder}"; FROM_SOURCE=1; shift ;;
	--tools)       TOOLS=1 ;;
 --browser) BROWSER=1; TOOLS=1 ;;
 --browser-agent) BROWSER_AGENT=1; BROWSER=1; TOOLS=1 ;;
	--no-setup)    SETUP=0 ;;
	--uninstall)   UNINSTALL=1 ;;
	--purge)       PURGE=1 ;;
	-h|--help)     sed -n '2,/^set -E/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; exit 0 ;;
	*)             die "unknown option: $1 (see --help)" ;;
	esac
	shift
done

# ---- where
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$OS" in linux|darwin) ;; *) die "Linux and macOS only (this is $OS). On Windows, use WSL2." ;; esac
case "$(uname -m)" in
	x86_64|amd64)  ARCH=amd64 ;;
	aarch64|arm64) ARCH=arm64 ;;
	*) die "unsupported CPU: $(uname -m)" ;;
esac
if [ -z "$PREFIX" ]; then
	if [ "$(id -u)" = 0 ]; then PREFIX=/usr/local/bin; else PREFIX="$HOME/.local/bin"; fi
fi
HOME_DIR="${MACRON_HOME:-$HOME/.macron}"
TARGET="$PREFIX/macron"

# ---- uninstall
if [ "$UNINSTALL" = 1 ]; then
	if [ -x "$TARGET" ]; then "$TARGET" service uninstall || true; fi
	rm -f "$TARGET" "$TARGET.old"
	ok "the program is removed"
	if [ "$PURGE" = 1 ]; then
		printf 'Remove %s (the database, notes, skills and files)? Type yes: ' "$HOME_DIR"
		read -r answer </dev/tty || answer=no
		if [ "$answer" = yes ]; then rm -rf -- "$HOME_DIR"; ok "$HOME_DIR is removed"; else say "kept $HOME_DIR"; fi
	else
		say "Your data in $HOME_DIR is untouched (--purge removes it)."
	fi
	exit 0
fi

# ---- download or build
TMP="$(mktemp -d)"
trap 'rm -rf -- "$TMP"' EXIT

fetch() { # url file
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

download_release() {
	local tag="$VERSION" api="https://api.github.com/repos/$REPO/releases"
	if [ -z "$tag" ]; then
		fetch "$api/latest" "$TMP/release.json" 2>/dev/null || return 1
		tag="$(sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' "$TMP/release.json" | head -n1)"
		[ -n "$tag" ] || return 1
	fi
	local ver="${tag#v}" name base
	name="macron-$ver-$OS-$ARCH.tar.gz"
	base="https://github.com/$REPO/releases/download/$tag"
	say "Downloading Macron Agent $ver for $OS/$ARCH…"
	fetch "$base/$name" "$TMP/$name" 2>/dev/null || return 1
	fetch "$base/SHA256SUMS" "$TMP/SHA256SUMS" 2>/dev/null || return 1
	local want got
	want="$(grep " $name\$" "$TMP/SHA256SUMS" | head -n1 | cut -d' ' -f1)"
	got="$(sha256_of "$TMP/$name")"
	[ -n "$want" ] && [ "$want" = "$got" ] || die "the download does not match its SHA-256 sum; nothing was installed"
	tar -xzf "$TMP/$name" -C "$TMP"
	cp "$TMP/macron-$ver-$OS-$ARCH/macron" "$TMP/macron"
	ok "checked the SHA-256 sum"
}

if [ -n "$BINARY" ]; then
	[ -f "$BINARY" ] || die "no such file: $BINARY"
	cp "$BINARY" "$TMP/macron"
elif [ "$FROM_SOURCE" = 1 ]; then
	build_from_source
elif ! download_release; then
	warn "no release to download for $OS/$ARCH (yet); building from source instead"
	build_from_source
fi

# ---- install
mkdir -p "$PREFIX"
# Validate before replacing the working program; private staging also avoids
# following a pre-existing macron.new symlink in the installation directory.
STAGE="$(mktemp -d "$PREFIX/.macron-install.XXXXXX")"
trap 'rm -rf -- "$TMP" "${STAGE:-}"' EXIT
install -m 0755 "$TMP/macron" "$STAGE/macron"
"$STAGE/macron" version >/dev/null || die "the new program does not start; the installed program was kept"
if [ -e "$TARGET" ]; then
 cp -p "$TARGET" "$STAGE/macron.old" || die "cannot back up the installed program"
 mv -f "$STAGE/macron.old" "$TARGET.old"
fi
mv -f "$STAGE/macron" "$TARGET"
ok "installed $("$TARGET" version) in $TARGET"
case ":$PATH:" in *":$PREFIX:"*) ;; *) warn "$PREFIX is not on your PATH. Add it:  echo 'export PATH=\"$PREFIX:\$PATH\"' >> ~/.bashrc" ;; esac

# ---- the tools the agent likes to have
if [ "$TOOLS" = 1 ]; then
	pkgs="python3 python3-pip python3-venv git curl ffmpeg jq unzip poppler-utils openssh-client sshpass rsync"
	if have apt-get; then
		sudo_cmd=""; [ "$(id -u)" = 0 ] || sudo_cmd="sudo"
		say "Installing: $pkgs"
		$sudo_cmd apt-get update -qq && $sudo_cmd apt-get install -y -qq $pkgs || warn "some packages did not install"
		$sudo_cmd apt-get install -y -qq gh >/dev/null 2>&1 || true   # GitHub's CLI: not in every release
	elif have brew; then
		brew install python git curl ffmpeg jq poppler rsync gh || warn "some packages did not install"
		brew install hudochenkov/sshpass/sshpass >/dev/null 2>&1 || warn "sshpass did not install: servers that need a password will need a key"
	else
		warn "--tools knows apt and Homebrew; install python3, git, curl, ffmpeg, ssh and sshpass yourself"
	fi
 # Keep Python packages out of the system interpreter (PEP 668).
 have python3 || die "--tools requires python3"
 python3 -c 'import sys; sys.exit(0 if sys.version_info >= (3, 12) else 1)' || die "the pinned Python tools require Python 3.12+"
 req="$SCRIPT_DIR/requirements-tools.lock"
 if [ -n "$SOURCE_DIR" ]; then req="$SOURCE_DIR/requirements-tools.lock"; fi
 if [ ! -f "$req" ]; then
  req="$TMP/requirements-tools.lock"
  fetch "https://raw.githubusercontent.com/$REPO/${VERSION:-main}/requirements-tools.lock" "$req"
 fi
 mkdir -p "$HOME_DIR"
 chmod 0700 "$HOME_DIR"
 python3 -m venv "$HOME_DIR/venv" || die "cannot create the tools environment (install python3-venv)"
 "$HOME_DIR/venv/bin/python" -m pip install --disable-pip-version-check --require-hashes -r "$req" || die "Python tool dependencies did not install"
 if [ "$BROWSER_AGENT" = 1 ]; then
  agent_req="$SCRIPT_DIR/requirements-browser-agent.lock"
  if [ -n "$SOURCE_DIR" ]; then agent_req="$SOURCE_DIR/requirements-browser-agent.lock"; fi
  if [ ! -f "$agent_req" ]; then agent_req="$TMP/requirements-browser-agent.lock"; fetch "https://raw.githubusercontent.com/$REPO/${VERSION:-main}/requirements-browser-agent.lock" "$agent_req"; fi
  python3 -m venv "$HOME_DIR/browser-agent"
  "$HOME_DIR/browser-agent/bin/python" -m pip install --disable-pip-version-check --require-hashes -r "$agent_req" || die "browser-agent dependencies did not install"
 fi
 ok "Python tools installed in $HOME_DIR/venv"
 if [ "$BROWSER" = 1 ]; then
  PLAYWRIGHT_BROWSERS_PATH="$HOME_DIR/browsers" "$HOME_DIR/venv/bin/python" -m playwright install chromium || die "Chromium install failed"
 fi
fi

# ---- setup
if [ "$SETUP" = 1 ]; then
	if [ -f "$HOME_DIR/config.env" ]; then
		ok "your settings in $HOME_DIR are kept"
		if "$TARGET" service status >/dev/null 2>&1; then "$TARGET" service restart >/dev/null 2>&1 && ok "the service was restarted on the new program"; fi
	elif [ -r /dev/tty ]; then
		say ""
		"$TARGET" setup </dev/tty
	else
		say ""
		say "Next:  macron setup"
	fi
fi
say ""
say "Macron Agent is installed.   macron doctor · macron service install · macron update"
