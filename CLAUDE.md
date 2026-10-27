# Bierdeckel – Hinweise für die Arbeit am Code

- Sprache: Oberfläche, Kommentare, Commits, Issues und PRs auf Deutsch.
- PRs immer gegen `main`; bauen PRs aufeinander auf, die Merge-Reihenfolge im PR-Text nennen.
- **Jede Änderung bekommt einen Eintrag in `CHANGELOG.md` unter `## Unveröffentlicht`** (Kategorien Neu, Verbessert, Behoben, Intern; mit PR-Nummer). Die Versionsnummer setzt der Release-Workflow beim Merge. Ausnahme nur mit Label `kein-changelog`.
- Vor jedem Commit: `gofmt -l .` (muss leer sein), `go vet ./...`, `go test -race ./...`.
- Keine neuen Abhängigkeiten ohne guten Grund (einzige: mattn/go-sqlite3; IMAP ist bewusst selbst implementiert). Kein JavaScript, CSP `default-src 'none'`, keine Inline-Styles.
- Migrationen in `db.go` nur anhängen, nie ändern.
- Datenquellen: Inserate kommen aus Suchauftrags-Mails (IMAP, nur lesend). Kein Scraping von Portalen mit Bot-Schutz und keine Umgehung von CAPTCHAs oder Bot-Erkennung.
- Parser-Änderungen immer mit Fixture in `testdata/` absichern; echte Mails vorher anonymisieren (Tracking-Tokens, Empfängeradresse).
- Das Dashboard ist privat: alles außer `/healthz` und `/static` hinter Basic Auth, POSTs nur same-origin.
