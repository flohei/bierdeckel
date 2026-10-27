#!/usr/bin/env bash
# Läuft auf dem Uberspace, aufgerufen vom Release-Workflow:
#   ssh user@host bash -s -- <version> < deploy.sh
# Erwartet das neue Binary unter ~/bierdeckel/releases/bierdeckel-<version>.new
set -euo pipefail

VERSION="$1"
APP="$HOME/bierdeckel"
NEW="$APP/releases/bierdeckel-$VERSION"
PORT="${BIERDECKEL_PORT:-8090}"

mv "$NEW.new" "$NEW"
chmod 755 "$NEW"
PREV="$(readlink "$APP/bierdeckel" || true)"

# Backup vor jedem Deploy, da neue Versionen Migrationen ausführen können. Die letzten 10 bleiben.
if [ -f "$APP/data/bierdeckel.db" ] && command -v sqlite3 >/dev/null; then
  sqlite3 "$APP/data/bierdeckel.db" ".backup '$APP/backups/bierdeckel-vor-$VERSION.db'"
  ls -1t "$APP"/backups/*.db 2>/dev/null | tail -n +11 | xargs -r rm --
fi

ln -sfn "$NEW" "$APP/bierdeckel"
supervisorctl restart bierdeckel

for _ in $(seq 1 20); do
  if curl -fsS "http://127.0.0.1:$PORT/healthz" 2>/dev/null | grep -q "$VERSION"; then
    echo "✓ $VERSION läuft"
    ls -1t "$APP"/releases/bierdeckel-* | tail -n +6 | xargs -r rm --
    exit 0
  fi
  sleep 1
done

echo "✗ Healthcheck fehlgeschlagen – Rollback auf ${PREV:-(keine Vorversion)}" >&2
supervisorctl tail bierdeckel stderr >&2 || true
if [ -n "$PREV" ]; then
  ln -sfn "$PREV" "$APP/bierdeckel"
  supervisorctl restart bierdeckel
fi
exit 1
