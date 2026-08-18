#!/bin/sh
# BlockPanel is fully contained in this folder — "uninstalling" just means
# stopping it and deleting the folder. This script stops the panel and tells
# you what to remove; with --purge it deletes the data directory too.
set -eu

DIR=$(cd "$(dirname "$0")" && pwd)

[ -x "$DIR/stop.sh" ] && "$DIR/stop.sh"

rm -f "$DIR/blockpanel" "$DIR/.panel.pid"

if [ "${1:-}" = "--purge" ]; then
  rm -rf "$DIR/data"
  echo "Panel binary and all data removed. Delete this folder to finish."
else
  echo "Panel binary removed; ./data (users, servers, worlds, backups) kept."
  echo "Delete this folder — or run with --purge — to remove everything."
fi
