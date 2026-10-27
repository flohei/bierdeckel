#!/usr/bin/env bash
# Einmalige Einrichtung von Bierdeckel auf einem Uberspace (U7).
#
#   bash setup.sh [domain]
#
# Ohne Domain wird <user>.uber.space verwendet. Danach liefert der GitHub-Workflow
# bei jedem Merge nach main eine neue Version aus.
set -euo pipefail

APP="$HOME/bierdeckel"
PORT="${BIERDECKEL_PORT:-8090}"
DOMAIN="${1:-$USER.uber.space}"

mkdir -p "$APP/releases" "$APP/data" "$APP/backups" "$HOME/etc/services.d"
chmod 700 "$APP" "$APP/data"

if [ ! -f "$APP/env" ]; then
  PASS="$(head -c 18 /dev/urandom | base64 | tr -d '/+=')"
  umask 077
  cat > "$APP/env" <<ENV
BIERDECKEL_ADDR=0.0.0.0:$PORT
BIERDECKEL_DATA=$APP/data
# Zugang zum Dashboard (HTTP Basic Auth)
BIERDECKEL_USER=$USER
BIERDECKEL_PASSWORD=$PASS
# Postfach mit den Suchauftrags-Mails (IMAPS, Port 993)
BIERDECKEL_IMAP_ADDR=
BIERDECKEL_IMAP_USER=
BIERDECKEL_IMAP_PASSWORD=
BIERDECKEL_IMAP_MAILBOX=INBOX
BIERDECKEL_INTERVAL=30m
ENV
  echo "→ $APP/env angelegt – Dashboard-Login: $USER / $PASS"
  echo "  IMAP-Zugangsdaten bitte dort eintragen."
fi

# Wrapper lädt die Konfiguration; auch für CLI-Befehle nutzbar: ~/bierdeckel/run.sh fetch
cat > "$APP/run.sh" <<'RUN'
#!/usr/bin/env bash
set -a; source "$HOME/bierdeckel/env"; set +a
exec "$HOME/bierdeckel/bierdeckel" "${@:-serve}"
RUN
chmod 755 "$APP/run.sh"

cat > "$HOME/etc/services.d/bierdeckel.ini" <<INI
[program:bierdeckel]
command=$APP/run.sh
autostart=true
autorestart=true
startsecs=3
stopsignal=TERM
INI

uberspace web backend set "$DOMAIN" --http --port "$PORT"
supervisorctl reread
supervisorctl update

echo
echo "Fertig. Der Dienst startet, sobald das erste Release deployt wurde."
