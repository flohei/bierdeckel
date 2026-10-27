# Bierdeckel

Privates Dashboard für Kapitalanlage-Wohnungen nach der **Bierdeckelrechnung**:

> Bruttomietrendite = Jahresnettokaltmiete ÷ Kaufpreis  (5 % ≙ Kaufpreisfaktor 20)

Bierdeckel liest die Suchauftrags-Mails von ImmoScout24, immowelt (und Kleinanzeigen) aus einem eigenen Postfach, setzt pro Stadt einen Mietspiegel-Wert an und zeigt alle Wohnungen über dem Schwellwert.

## Warum Mails statt Scraping?

ImmoScout24 schließt automatisierten Zugriff in den Nutzungsbedingungen aus und blockt Bots aktiv. Suchaufträge sind der offizielle Weg, über neue Inserate informiert zu werden – und Mails ändern ihr Layout seltener als Webseiten. Der Parser verlässt sich nur auf Exposé-Links und Zahlen mit Einheit, nicht auf HTML-Strukturen.

## Einrichtung

1. **Postfach:** Eine eigene Adresse (z. B. `immo@deine-domain.de`) nur für die Alerts anlegen – oder einen Ordner im bestehenden Postfach, in den eine Regel die Alert-Mails verschiebt.
2. **Suchaufträge:** Bei ImmoScout24 und immowelt pro Stadt einen Suchauftrag „Wohnung kaufen“ mit sofortiger Benachrichtigung an diese Adresse anlegen. Grobe Filter (Preis, Fläche) reduzieren Rauschen; die Rendite filtert Bierdeckel.
3. **Städte:** Im Dashboard unter „Städte“ jede Stadt mit Mietspiegel-Wert (€/m² nettokalt) und optional Postleitzahlen anlegen.

## Konfiguration (Umgebungsvariablen)

| Variable | Standard | Bedeutung |
|---|---|---|
| `BIERDECKEL_ADDR` | `127.0.0.1:8080` | Adresse des Webservers |
| `BIERDECKEL_DATA` | `./data` | Verzeichnis für die SQLite-Datenbank |
| `BIERDECKEL_USER`, `BIERDECKEL_PASSWORD` | – | Basic Auth. **Pflicht**, sobald nicht nur auf localhost gelauscht wird |
| `BIERDECKEL_IMAP_ADDR` | – | IMAPS-Server mit Port, z. B. `mail.example.org:993` |
| `BIERDECKEL_IMAP_USER`, `BIERDECKEL_IMAP_PASSWORD` | – | Zugangsdaten des Alert-Postfachs |
| `BIERDECKEL_IMAP_MAILBOX` | `INBOX` | Ordner mit den Alerts |
| `BIERDECKEL_INTERVAL` | `30m` | Abrufintervall |

Das Postfach wird nur gelesen (`EXAMINE`, `BODY.PEEK`): Mails bleiben ungelesen und unverändert. Bierdeckel merkt sich die zuletzt verarbeitete UID.

## Lokal starten

```sh
go run .                                # http://127.0.0.1:8080, ohne Passwort
go run . parse alert.eml                # prüfen, was der Parser in einer Mail erkennt
go run . import alerts/*.eml            # gespeicherte Mails einlesen
```

Neue Mail-Layouts: Mail als `.eml` speichern, mit `parse` prüfen, anonymisiert nach `testdata/` legen und einen Test ergänzen. Die Fixtures in `testdata/` sind bisher **synthetisch** nachgebaut.

## Deploy auf Uberspace

Wie bei Lesestoff: einmalig `deploy/uberspace/setup.sh` auf dem Uberspace ausführen (legt `~/bierdeckel/env` mit zufälligem Dashboard-Passwort an, Port 8090), IMAP-Daten in `~/bierdeckel/env` eintragen. Im Repo die Variablen `UBERSPACE_HOST`, `UBERSPACE_USER` und die Secrets `UBERSPACE_SSH_KEY` (optional `UBERSPACE_KNOWN_HOSTS`) setzen, Environment `uberspace` anlegen. Jeder Merge nach `main` erzeugt ein Release und deployt es.

Wer das Dashboard gar nicht öffentlich erreichbar haben will: `BIERDECKEL_ADDR=127.0.0.1:8090` lassen, keinen Web-Backend setzen und per SSH-Tunnel zugreifen (`ssh -L 8090:127.0.0.1:8090 uberspace`).

## Grenzen

- Mietspiegel-Werte sind Durchschnittswerte; Lage, Baujahr und Zustand verschieben die erzielbare Miete deutlich. Die Bierdeckelrechnung ist ein Filter, keine Bewertung.
- Inserate verschwinden nicht automatisch, wenn sie beim Portal offline gehen – „zuletzt gesehen“ zeigt, wann sie zuletzt in einer Mail auftauchten.
