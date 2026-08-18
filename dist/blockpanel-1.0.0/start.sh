#!/bin/sh
# Start BlockPanel in the background, fully contained in this folder.
set -eu

DIR=$(cd "$(dirname "$0")" && pwd)
PIDFILE="$DIR/.panel.pid"
LOG="$DIR/data/logs/panel.log"

[ -x "$DIR/blockpanel" ] || { echo "run ./install.sh first"; exit 1; }

if [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
  echo "already running (pid $(cat "$PIDFILE"))"
  exit 0
fi

mkdir -p "$DIR/data/logs"
nohup "$DIR/blockpanel" --data "$DIR/data" >> "$LOG" 2>&1 &
echo $! > "$PIDFILE"
sleep 1
if ! kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
  rm -f "$PIDFILE"
  echo "failed to start — last log lines:"
  tail -20 "$LOG" 2>/dev/null || true
  exit 1
fi

# Work out the URL from config.json (defaults on first run: https, 8443).
PORT=8443
SCHEME=https
if [ -f "$DIR/data/config.json" ]; then
  P=$(sed -n 's/.*"port": *\([0-9][0-9]*\).*/\1/p' "$DIR/data/config.json" | head -1)
  [ -n "$P" ] && PORT=$P
  grep -q '"mode": *"http"' "$DIR/data/config.json" && SCHEME=http
fi

echo "BlockPanel running (pid $(cat "$PIDFILE")) — $SCHEME://localhost:$PORT"
echo "logs: $LOG"
