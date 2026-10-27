# Änderungen

Alle nennenswerten Änderungen an Bierdeckel. Neue Einträge kommen unter **Unveröffentlicht**; beim Release wird daraus automatisch die Versionsnummer.

Kategorien: **Neu** · **Verbessert** · **Behoben** · **Intern**

## Unveröffentlicht

## v0.1.4 – 27.10.2026

### Behoben
- immowelt-Mails im aktuellen Layout: Titel, Ort und PLZ werden richtig erkannt (Ort über mehrere Zeilen, „(85055)“), Link führt zum Inserat (#2)
- Anbieter (Makler, Banken, Bauträger) und Mail-Texte wie „Deine ersten Treffer zur Suche“ landen nicht mehr als Ort in den Städte-Vorschlägen (#2)

### Neu
- `bierdeckel reimport` liest nach Parser-Verbesserungen alle Mails neu ein; Inserate mit Notiz bleiben erhalten (#2)

## v0.1.3 – 27.10.2026

### Neu
- Städte-Seite schlägt Orte aus eingelesenen Inseraten vor, die noch keiner Stadt zugeordnet sind – mit PLZ, Anzahl und Übernahme per Klick inkl. Mietspiegel (#1)

## v0.1.1 – 27.10.2026

### Neu
- Dashboard mit allen Inseraten über einer Mindest-Bruttomietrendite (Standard 5 %), sortierbar nach Rendite, Datum, Preis und €/m², mit Kaufpreisfaktor und Preissenkungen
- Städteliste mit Postleitzahlen und Mietspiegel (€/m² nettokalt); Inserate werden über PLZ oder Ortsnamen zugeordnet
- Inserate aus Suchauftrags-Mails von ImmoScout24, immowelt und Kleinanzeigen per IMAP (nur lesend, regelmäßig und auf Knopfdruck)
- Ist-Miete aus dem Inserat oder manuell eingetragen hat Vorrang vor dem Mietspiegel
- Inserate manuell hinzufügen, korrigieren, ausblenden und mit Notiz versehen
- CLI: `fetch`, `import <datei.eml>` und `parse <datei.eml>` zum Testen des Parsers

### Intern
- Go + SQLite, ein statisches Binary; CI, Release bei Merge nach `main` und Uberspace-Deploy wie bei Lesestoff
