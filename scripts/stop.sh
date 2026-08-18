#!/bin/sh
# Stop BlockPanel. SIGTERM makes the panel stop every running Minecraft
# server gracefully (stop command -> grace period) before it exits, so this
# can take a couple of minutes with big servers.
set -eu

DIR=$(cd "$(dirname "$0")" && pwd)
PIDFILE="$DIR/.panel.pid"

if [ ! -f "$PIDFILE" ]; then
  echo "not running (no pid file)"
  exit 0
fi
PID=$(cat "$PIDFILE")
if ! kill -0 "$PID" 2>/dev/null; then
  echo "not running (stale pid file removed)"
  rm -f "$PIDFILE"
  exit 0
fi

echo "stopping BlockPanel (pid $PID) — waiting for Minecraft servers to shut down…"
kill -TERM "$PID"
i=0
while kill -0 "$PID" 2>/dev/null; do
  i=$((i + 1))
  if [ "$i" -ge 180 ]; then
    echo "still running after 180s, sending SIGKILL"
    kill -KILL "$PID" 2>/dev/null || true
    break
  fi
  sleep 1
done
rm -f "$PIDFILE"
echo "stopped"
