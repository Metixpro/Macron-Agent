#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
	cat <<'USAGE'
Macron Agent installer

  curl -fsSL https://raw.githubusercontent.com/Metixpro/macron-agent/main/install.sh | bash
  curl -fsSL …/install.sh | bash -s -- [OPTIONS]

Options:
  --version vX.Y.Z   install that release instead of the newest
  --prefix DIR       put the program in DIR (default: /usr/local/bin as root, else ~/.local/bin)
  --binary FILE      install this program instead of downloading one
  --from-source      build it here (needs git and Go) instead of downloading a release
  --source-dir DIR   build a local source checkout (no GitHub clone)
  --no-tools         skip the system packages and the Python tools (installed by default)
  --tools            (default) install python3, git, curl, ffmpeg, jq, poppler, ssh, sshpass, rsync, gh
  --browser          also download Chromium into the private home
  --browser-agent    also install the autonomous browser runtime in a separate venv
  --no-service       do not install or restart the background service
  --no-setup         only install the program (no questions)
  -y, --yes          ask nothing; print the exact next command at the end
  --uninstall        remove the service and the program (your data in ~/.macron stays)
  --purge            with --uninstall: remove ~/.macron too, after asking
  -h, --help         this help
USAGE
}

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
TOOLS=1
TOOLS_ASKED=0
SERVICE=1
ASSUME_YES=0
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
	--source-dir)  SOURCE_DIR="${2:?--source-dir needs a folder}"; FROM_SOURCE=1; shift ;;
	--tools)       TOOLS=1; TOOLS_ASKED=1 ;;
	--no-tools)    TOOLS=0 ;;
	--browser)     BROWSER=1; TOOLS=1; TOOLS_ASKED=1 ;;
	--browser-agent) BROWSER_AGENT=1; BROWSER=1; TOOLS=1; TOOLS_ASKED=1 ;;
	--no-service)  SERVICE=0 ;;
	--no-setup)    SETUP=0 ;;
	-y|--yes)      ASSUME_YES=1 ;;
	--uninstall)   UNINSTALL=1 ;;
	--purge)       PURGE=1 ;;
	-h|--help)     usage; exit 0 ;;
	*)             die "unknown option: $1 (see --help)" ;;
	esac
	shift
done

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

TMP="$(mktemp -d)"
trap 'rm -rf -- "$TMP"' EXIT

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
	local ver="${tag#v}" name base
	name="macron-$ver-$OS-$ARCH.tar.gz"
	base="https://github.com/$REPO/releases/download/$tag"
	say "Downloading Macron Agent $ver for $OS/$ARCH…"
	fetch_release_asset "$base/$name" "$name" "$TMP/$name" "$api" "$tag" || return 1
	fetch_release_asset "$base/SHA256SUMS" "SHA256SUMS" "$TMP/SHA256SUMS" "$api" "$tag" || return 1
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

mkdir -p "$PREFIX"
STAGE="$(mktemp -d "$PREFIX/.macron-install.XXXXXX")"
trap 'rm -rf -- "$TMP" "${STAGE:-}"' EXIT
install -m 0755 "$TMP/macron" "$STAGE/macron"
"$STAGE/macron" version >/dev/null || die "the new program does not start; the installed program was kept"
OLD_VERSION=""
if [ -x "$TARGET" ]; then OLD_VERSION="$("$TARGET" version 2>/dev/null | awk '{print $NF}' || true)"; fi
if [ -e "$TARGET" ]; then
 cp -p "$TARGET" "$STAGE/macron.old" || die "cannot back up the installed program"
 mv -f "$STAGE/macron.old" "$TARGET.old"
fi
mv -f "$STAGE/macron" "$TARGET"
NEW_VERSION="$("$TARGET" version | awk '{print $NF}')"
if [ -n "$OLD_VERSION" ] && [ "$OLD_VERSION" != "$NEW_VERSION" ]; then
	ok "updated $OLD_VERSION → $NEW_VERSION in $TARGET"
else
	ok "installed macron $NEW_VERSION in $TARGET"
fi
case ":$PATH:" in *":$PREFIX:"*) ;; *) warn "$PREFIX is not on your PATH. Add it:  echo 'export PATH=\"$PREFIX:\$PATH\"' >> ~/.bashrc" ;; esac

SUDO=""
if [ "$(id -u)" != 0 ]; then
	if have sudo && { sudo -n true 2>/dev/null || [ -r /dev/tty ]; }; then SUDO="sudo"; else SUDO="none"; fi
fi
python_ok() { "$1" -c 'import sys; sys.exit(0 if sys.version_info >= (3, 12) else 1)' 2>/dev/null; }
python_hint() {
	say "   Python 3.12+ is needed for the pinned tools. Ways to get it:"
	say "     Ubuntu 22.04:   sudo add-apt-repository ppa:deadsnakes/ppa && sudo apt install python3.12 python3.12-venv"
	say "     Debian 12:      curl -LsSf https://astral.sh/uv/install.sh | sh && uv python install 3.12"
	say "     macOS:          brew install python@3.12"
	say "   Then run this installer again."
}
tools_failed() {
	if [ "$TOOLS_ASKED" = 1 ]; then die "$1"; fi
	warn "$1 — continuing without the Python tools"
}
install_tools() {
	pkgs="python3 python3-pip python3-venv git curl ffmpeg jq unzip poppler-utils openssh-client sshpass rsync"
	if [ "$SUDO" = none ]; then
		warn "no root and no sudo: skipping system packages ($pkgs)"
	elif have apt-get; then
		say "Installing: $pkgs"
		{ $SUDO apt-get update -qq && DEBIAN_FRONTEND=noninteractive $SUDO apt-get install -y -qq $pkgs; } </dev/null || warn "some packages did not install"
		DEBIAN_FRONTEND=noninteractive $SUDO apt-get install -y -qq gh </dev/null >/dev/null 2>&1 || true
	elif have dnf; then
		$SUDO dnf install -y -q python3 python3-pip git curl ffmpeg jq unzip poppler-utils openssh-clients sshpass rsync gh </dev/null || warn "some packages did not install"
	elif have brew; then
		brew install python git curl ffmpeg jq poppler rsync gh || warn "some packages did not install"
		brew install hudochenkov/sshpass/sshpass >/dev/null 2>&1 || warn "sshpass did not install: servers that need a password will need a key"
	else
		warn "only apt, dnf and Homebrew are known; install python3, git, curl, ffmpeg, ssh and sshpass yourself"
	fi
	PY=""
	for c in python3 python3.13 python3.12; do
		if have "$c" && python_ok "$c"; then PY="$c"; break; fi
	done
	if [ -z "$PY" ]; then
		if have python3; then warn "python3 is $(python3 -c 'import platform; print(platform.python_version())') (older than 3.12)"; else warn "python3 is missing"; fi
		python_hint
		tools_failed "the pinned Python tools need Python 3.12+"
		return 0
	fi
	req="$SCRIPT_DIR/requirements-tools.lock"
	if [ -n "$SOURCE_DIR" ]; then req="$SOURCE_DIR/requirements-tools.lock"; fi
	if [ ! -f "$req" ]; then
		req="$TMP/requirements-tools.lock"
		fetch "https://raw.githubusercontent.com/$REPO/${VERSION:-main}/requirements-tools.lock" "$req" || { tools_failed "cannot download requirements-tools.lock"; return 0; }
	fi
	mkdir -p "$HOME_DIR"
	chmod 0700 "$HOME_DIR"
	"$PY" -m venv "$HOME_DIR/venv" || { tools_failed "cannot create the tools environment (install python3-venv)"; return 0; }
	"$HOME_DIR/venv/bin/python" -m pip install -q --disable-pip-version-check --require-hashes -r "$req" || { tools_failed "Python tool dependencies did not install"; return 0; }
	if [ "$BROWSER_AGENT" = 1 ]; then
		agent_req="$SCRIPT_DIR/requirements-browser-agent.lock"
		if [ -n "$SOURCE_DIR" ]; then agent_req="$SOURCE_DIR/requirements-browser-agent.lock"; fi
		if [ ! -f "$agent_req" ]; then agent_req="$TMP/requirements-browser-agent.lock"; fetch "https://raw.githubusercontent.com/$REPO/${VERSION:-main}/requirements-browser-agent.lock" "$agent_req"; fi
		"$PY" -m venv "$HOME_DIR/browser-agent"
		"$HOME_DIR/browser-agent/bin/python" -m pip install -q --disable-pip-version-check --require-hashes -r "$agent_req" || die "browser-agent dependencies did not install"
	fi
	ok "Python tools installed in $HOME_DIR/venv ($("$HOME_DIR/venv/bin/python" -c 'import platform; print(platform.python_version())'))"
	if [ "$BROWSER" = 1 ]; then
		PLAYWRIGHT_BROWSERS_PATH="$HOME_DIR/browsers" "$HOME_DIR/venv/bin/python" -m playwright install chromium || die "Chromium install failed"
	fi
}
if [ "$TOOLS" = 1 ]; then install_tools; fi

INTERACTIVE=0
if [ "$ASSUME_YES" = 0 ] && [ -r /dev/tty ] && { : </dev/tty; } 2>/dev/null; then INTERACTIVE=1; fi

ME="${USER:-$(id -un)}"
can_service() { [ "$SERVICE" = 1 ] && { [ "$OS" = darwin ] || have systemctl; }; }

start_service() {
	can_service || return 0
	if "$TARGET" service install; then
		if [ "$OS" = linux ] && [ "$(id -u)" != 0 ]; then
			loginctl show-user "$ME" -p Linger 2>/dev/null | grep -q yes \
				|| loginctl enable-linger "$ME" 2>/dev/null \
				|| { [ "$SUDO" = sudo ] && sudo loginctl enable-linger "$ME"; } \
				|| warn "could not enable lingering; run: sudo loginctl enable-linger $ME"
		fi
		"$TARGET" service status >/dev/null 2>&1 && ok "the service is active (macron $NEW_VERSION)"
	else
		warn "the service did not start; see: macron service status · macron logs"
	fi
}

NEXT=""
if [ "$SETUP" = 1 ]; then
	if [ -f "$HOME_DIR/config.env" ]; then
		ok "your settings in $HOME_DIR are kept"
		start_service
	elif [ "$INTERACTIVE" = 1 ]; then
		say ""
		if "$TARGET" setup --no-service </dev/tty; then start_service; else NEXT="macron setup"; fi
	else
		NEXT="macron setup"
	fi
fi
if [ -z "$NEXT" ] && [ "$SETUP" = 0 ] && [ ! -f "$HOME_DIR/config.env" ]; then NEXT="macron setup"; fi
if [ -n "$NEXT" ] && can_service; then NEXT="$NEXT && macron service install"; fi
case ":$PATH:" in *":$PREFIX:"*) ;; *) if [ -n "$NEXT" ]; then NEXT="export PATH=\"$PREFIX:\$PATH\"; $NEXT"; fi ;; esac
say ""
if [ -n "$NEXT" ]; then
	say "Macron Agent $NEW_VERSION is installed. Next, run:"
	say ""
	say "    $NEXT"
else
	say "Macron Agent $NEW_VERSION is ready.   macron doctor · macron service status · macron update"
fi
