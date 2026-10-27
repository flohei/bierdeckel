#!/usr/bin/env bash
# Pflege von CHANGELOG.md beim Release.
#   changelog.sh notes                 – Inhalt von „## Unveröffentlicht“ ausgeben (für die Release-Notizen)
#   changelog.sh release <VERSION> <DATUM> – „Unveröffentlicht“ in „## <VERSION> – <DATUM>“ umwandeln
#                                        und einen neuen, leeren Abschnitt „Unveröffentlicht“ anlegen
set -euo pipefail
FILE="${CHANGELOG:-CHANGELOG.md}"
HEAD="## Unveröffentlicht"

notes() {
  awk -v head="$HEAD" '
    $0 == head { on = 1; next }
    on && /^## / { exit }
    on { print }
  ' "$FILE" | sed -e '/./,$!d' | sed -e ':a' -e '/^\n*$/{$d;N;ba' -e '}'
}

case "${1:-}" in
  notes)
    notes
    ;;
  release)
    version="$2"; date="$3"
    if [ -z "$(notes | tr -d '[:space:]')" ]; then
      echo "Keine Einträge unter „Unveröffentlicht“ – nichts zu tun." >&2
      exit 0
    fi
    awk -v head="$HEAD" -v ver="## $version – $date" '
      $0 == head && !done { print head; print ""; print ver; done = 1; next }
      { print }
    ' "$FILE" > "$FILE.tmp" && mv "$FILE.tmp" "$FILE"
    ;;
  *)
    echo "Verwendung: $0 notes | release <VERSION> <DATUM>" >&2
    exit 2
    ;;
esac
