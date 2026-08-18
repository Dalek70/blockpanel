#!/bin/sh
# BlockPanel portable setup — macOS & Linux.
# Everything stays INSIDE this extracted folder: the binary, the config, the
# servers, the worlds, the backups, the logs. Nothing is written anywhere
# else on the system (no launchd, no systemd, no dotfiles in $HOME).
#
# Run:  ./install.sh      then      ./start.sh
set -eu

say() { printf '%s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

DIR=$(cd "$(dirname "$0")" && pwd)

OS=$(uname -s)
ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64) GOARCH=amd64 ;;
  arm64|aarch64) GOARCH=arm64 ;;
  *) die "unsupported architecture: $ARCH" ;;
esac
case "$OS" in
  Darwin) GOOS=darwin ;;
  Linux)  GOOS=linux ;;
  *) die "unsupported OS: $OS (macOS and Linux only)" ;;
esac

BIN="$DIR/bin/blockpanel-$GOOS-$GOARCH"
[ -f "$BIN" ] || die "binary $BIN not found in this folder"

cp "$BIN" "$DIR/blockpanel"
chmod +x "$DIR/blockpanel"
mkdir -p "$DIR/data/logs"

if ! command -v java >/dev/null 2>&1; then
  say "note: 'java' was not found on PATH. The panel will run, but Minecraft"
  say "      servers need Java installed (e.g. Temurin 21) before they can start."
fi

say "BlockPanel is set up in: $DIR"
say ""
say "  ./start.sh   start the panel (background)"
say "  ./stop.sh    stop the panel (stops Minecraft servers gracefully first)"
say "  Logs:        $DIR/data/logs/panel.log"
say "  Everything (config, users, servers, worlds, backups) lives in ./data"
say ""
say "Because nothing is installed system-wide, the panel does NOT start at"
say "boot by itself — run ./start.sh after a reboot (or wire it into your own"
say "service manager; see README)."
say ""
say "Next: ./start.sh, then open https://localhost:8443 and create the admin"
say "account. (Self-signed certificate — the first-visit browser warning is"
say "expected.)"
