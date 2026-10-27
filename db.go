package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// migrations werden der Reihe nach ausgeführt; PRAGMA user_version merkt sich den Stand.
// Bestehende Einträge niemals ändern – nur neue anhängen.
var migrations = []string{
	`
CREATE TABLE cities (
	id          INTEGER PRIMARY KEY,
	name        TEXT NOT NULL UNIQUE COLLATE NOCASE,
	postcodes   TEXT NOT NULL DEFAULT '',  -- kommagetrennt, optional
	rent_per_m2 REAL,                      -- Mietspiegel, €/m² nettokalt; NULL = unbekannt
	active      INTEGER NOT NULL DEFAULT 1,
	note        TEXT NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL
);

CREATE TABLE listings (
	id          INTEGER PRIMARY KEY,
	source      TEXT NOT NULL,             -- is24 | immowelt | manual
	external_id TEXT NOT NULL,             -- Exposé-ID beim Portal
	url         TEXT NOT NULL,
	title       TEXT NOT NULL DEFAULT '',
	address     TEXT NOT NULL DEFAULT '',  -- Ortsangabe aus dem Inserat
	postcode    TEXT NOT NULL DEFAULT '',
	price       INTEGER NOT NULL DEFAULT 0, -- Kaufpreis in €
	first_price INTEGER NOT NULL DEFAULT 0,
	area        REAL NOT NULL DEFAULT 0,    -- Wohnfläche in m²
	rooms       REAL NOT NULL DEFAULT 0,
	rent_actual INTEGER,                    -- Ist-Miete nettokalt €/Monat, falls bekannt
	hidden      INTEGER NOT NULL DEFAULT 0,
	note        TEXT NOT NULL DEFAULT '',
	first_seen  INTEGER NOT NULL,
	last_seen   INTEGER NOT NULL,
	UNIQUE (source, external_id)
);
CREATE INDEX listings_seen ON listings(first_seen);

CREATE TABLE settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

-- Stand des IMAP-Abrufs: nur Nachrichten mit größerer UID werden gelesen.
CREATE TABLE mail_state (
	mailbox     TEXT PRIMARY KEY,
	uidvalidity INTEGER NOT NULL,
	last_uid    INTEGER NOT NULL
);

CREATE TABLE fetch_log (
	id           INTEGER PRIMARY KEY,
	started_at   INTEGER NOT NULL,
	finished_at  INTEGER NOT NULL,
	messages     INTEGER NOT NULL DEFAULT 0,
	listings_new INTEGER NOT NULL DEFAULT 0,
	listings_upd INTEGER NOT NULL DEFAULT 0,
	error        TEXT NOT NULL DEFAULT ''
);
`,
	`
ALTER TABLE listings ADD COLUMN favorite INTEGER NOT NULL DEFAULT 0;
CREATE INDEX listings_favorite ON listings(favorite) WHERE favorite = 1;
`,
}

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", "file:"+path+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("Migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Store kapselt alle Datenbankzugriffe.
type Store struct {
	db  *sql.DB
	now func() time.Time // für Tests
}

func (s *Store) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// ---- Städte ----

type City struct {
	ID        int64
	Name      string
	Postcodes []string
	RentPerM2 float64 // 0 = unbekannt
	Active    bool
	Note      string
}

func (s *Store) Cities() ([]City, error) {
	rows, err := s.db.Query(`SELECT id, name, postcodes, COALESCE(rent_per_m2, 0), active, note FROM cities ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []City
	for rows.Next() {
		var c City
		var pcs string
		if err := rows.Scan(&c.ID, &c.Name, &pcs, &c.RentPerM2, &c.Active, &c.Note); err != nil {
			return nil, err
		}
		c.Postcodes = splitList(pcs)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) City(id int64) (City, error) {
	cs, err := s.Cities()
	if err != nil {
		return City{}, err
	}
	for _, c := range cs {
		if c.ID == id {
			return c, nil
		}
	}
	return City{}, sql.ErrNoRows
}

var errDuplicateCity = errors.New("Stadt gibt es schon")

// SaveCity legt eine Stadt an (ID 0) oder aktualisiert sie.
func (s *Store) SaveCity(c *City) error {
	var rent any
	if c.RentPerM2 > 0 {
		rent = c.RentPerM2
	}
	pcs := strings.Join(c.Postcodes, ", ")
	var err error
	if c.ID == 0 {
		var res sql.Result
		res, err = s.db.Exec(`INSERT INTO cities (name, postcodes, rent_per_m2, active, note, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			c.Name, pcs, rent, c.Active, c.Note, s.clock().Unix())
		if err == nil {
			c.ID, err = res.LastInsertId()
		}
	} else {
		_, err = s.db.Exec(`UPDATE cities SET name = ?, postcodes = ?, rent_per_m2 = ?, active = ?, note = ? WHERE id = ?`,
			c.Name, pcs, rent, c.Active, c.Note, c.ID)
	}
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return errDuplicateCity
	}
	return err
}

func (s *Store) DeleteCity(id int64) error {
	_, err := s.db.Exec(`DELETE FROM cities WHERE id = ?`, id)
	return err
}

// ---- Inserate ----

type Listing struct {
	ID         int64
	Source     string
	ExternalID string
	URL        string
	Title      string
	Address    string
	Postcode   string
	Price      int64
	FirstPrice int64
	Area       float64
	Rooms      float64
	RentActual int64 // 0 = unbekannt
	Hidden     bool
	Favorite   bool
	Note       string
	FirstSeen  time.Time
	LastSeen   time.Time
}

// UpsertListing speichert ein Inserat. Bekannte Inserate bekommen den aktuellen Preis
// und füllen nur leere Felder auf. Rückgabe: true, wenn das Inserat neu ist.
func (s *Store) UpsertListing(l Listing) (bool, error) {
	if l.Source == "" || l.ExternalID == "" {
		return false, errors.New("Inserat ohne Quelle oder ID")
	}
	now := s.clock().Unix()
	var rent any
	if l.RentActual > 0 {
		rent = l.RentActual
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRow(`SELECT id FROM listings WHERE source = ? AND external_id = ?`, l.Source, l.ExternalID).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.Exec(`INSERT INTO listings (source, external_id, url, title, address, postcode, price, first_price, area, rooms, rent_actual, first_seen, last_seen)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			l.Source, l.ExternalID, l.URL, l.Title, l.Address, l.Postcode, l.Price, l.Price, l.Area, l.Rooms, rent, now, now)
		if err != nil {
			return false, err
		}
		return true, tx.Commit()
	case err != nil:
		return false, err
	}
	_, err = tx.Exec(`UPDATE listings SET
		last_seen   = ?,
		price       = CASE WHEN ? > 0 THEN ? ELSE price END,
		first_price = CASE WHEN first_price = 0 THEN ? ELSE first_price END,
		url         = CASE WHEN url = '' OR (? LIKE '%/expose/%' AND url NOT LIKE '%/expose/%') THEN ? ELSE url END,
		title       = CASE WHEN ? != '' THEN ? ELSE title END,
		address     = CASE WHEN address = '' THEN ? ELSE address END,
		postcode    = CASE WHEN postcode = '' THEN ? ELSE postcode END,
		area        = CASE WHEN area = 0 THEN ? ELSE area END,
		rooms       = CASE WHEN rooms = 0 THEN ? ELSE rooms END,
		rent_actual = COALESCE(?, rent_actual)
		WHERE id = ?`,
		now, l.Price, l.Price, l.Price, l.URL, l.URL, l.Title, l.Title, l.Address, l.Postcode, l.Area, l.Rooms, rent, id)
	if err != nil {
		return false, err
	}
	return false, tx.Commit()
}

const listingCols = `id, source, external_id, url, title, address, postcode, price, first_price, area, rooms, COALESCE(rent_actual, 0), hidden, favorite, note, first_seen, last_seen`

func scanListing(sc interface{ Scan(...any) error }) (Listing, error) {
	var l Listing
	var first, last int64
	err := sc.Scan(&l.ID, &l.Source, &l.ExternalID, &l.URL, &l.Title, &l.Address, &l.Postcode, &l.Price, &l.FirstPrice,
		&l.Area, &l.Rooms, &l.RentActual, &l.Hidden, &l.Favorite, &l.Note, &first, &last)
	l.FirstSeen, l.LastSeen = time.Unix(first, 0), time.Unix(last, 0)
	return l, err
}

func (s *Store) Listings() ([]Listing, error) {
	rows, err := s.db.Query(`SELECT ` + listingCols + ` FROM listings ORDER BY first_seen DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Listing
	for rows.Next() {
		l, err := scanListing(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) Listing(id int64) (Listing, error) {
	return scanListing(s.db.QueryRow(`SELECT `+listingCols+` FROM listings WHERE id = ?`, id))
}

func (s *Store) SetFavorite(id int64, fav bool) error {
	_, err := s.db.Exec(`UPDATE listings SET favorite = ? WHERE id = ?`, fav, id)
	return err
}

func (s *Store) SetHidden(id int64, hidden bool) error {
	_, err := s.db.Exec(`UPDATE listings SET hidden = ? WHERE id = ?`, hidden, id)
	return err
}

// UpdateListingManual übernimmt Korrekturen aus dem Formular (Parser-Fehler, Ist-Miete, Notiz).
func (s *Store) UpdateListingManual(l Listing) error {
	var rent any
	if l.RentActual > 0 {
		rent = l.RentActual
	}
	_, err := s.db.Exec(`UPDATE listings SET url = ?, title = ?, address = ?, postcode = ?, price = ?, area = ?, rooms = ?, rent_actual = ?, note = ? WHERE id = ?`,
		l.URL, l.Title, l.Address, l.Postcode, l.Price, l.Area, l.Rooms, rent, l.Note, l.ID)
	return err
}

func (s *Store) DeleteListing(id int64) error {
	_, err := s.db.Exec(`DELETE FROM listings WHERE id = ?`, id)
	return err
}

// ResetImported löscht alle aus Mails gelesenen Inserate ohne Notiz und setzt den
// IMAP-Stand zurück, damit der nächste Abruf alles mit dem aktuellen Parser neu einliest.
// Manuell angelegte Inserate, Favoriten und solche mit Notiz bleiben erhalten.
func (s *Store) ResetImported() (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM listings WHERE source != 'manual' AND note = '' AND favorite = 0`)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`DELETE FROM mail_state`); err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, tx.Commit()
}

// ---- Einstellungen ----

const defaultThreshold = 5.0

func (s *Store) setting(key, def string) string {
	var v string
	if err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v); err != nil {
		return def
	}
	return v
}

func (s *Store) setSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// Threshold ist die Mindest-Bruttomietrendite in Prozent.
func (s *Store) Threshold() float64 {
	v, err := strconv.ParseFloat(s.setting("threshold", ""), 64)
	if err != nil || v <= 0 {
		return defaultThreshold
	}
	return v
}

func (s *Store) SetThreshold(v float64) error {
	return s.setSetting("threshold", strconv.FormatFloat(v, 'f', -1, 64))
}

// ---- IMAP-Stand und Abruf-Protokoll ----

func (s *Store) MailState(mailbox string) (uidvalidity, lastUID uint32, err error) {
	err = s.db.QueryRow(`SELECT uidvalidity, last_uid FROM mail_state WHERE mailbox = ?`, mailbox).Scan(&uidvalidity, &lastUID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, nil
	}
	return
}

func (s *Store) SetMailState(mailbox string, uidvalidity, lastUID uint32) error {
	_, err := s.db.Exec(`INSERT INTO mail_state (mailbox, uidvalidity, last_uid) VALUES (?, ?, ?)
		ON CONFLICT(mailbox) DO UPDATE SET uidvalidity = excluded.uidvalidity, last_uid = excluded.last_uid`, mailbox, uidvalidity, lastUID)
	return err
}

type FetchResult struct {
	Started, Finished time.Time
	Messages          int
	New, Updated      int
	Err               error
	ErrText           string // aus der Datenbank gelesen
}

func (r FetchResult) Summary() string {
	s := fmt.Sprintf("%d Mails, %d neue und %d aktualisierte Inserate", r.Messages, r.New, r.Updated)
	if r.Err != nil {
		s += " – Fehler: " + r.Err.Error()
	}
	return s
}

func (s *Store) LogFetch(r FetchResult) error {
	msg := ""
	if r.Err != nil {
		msg = r.Err.Error()
	}
	_, err := s.db.Exec(`INSERT INTO fetch_log (started_at, finished_at, messages, listings_new, listings_upd, error) VALUES (?, ?, ?, ?, ?, ?)`,
		r.Started.Unix(), r.Finished.Unix(), r.Messages, r.New, r.Updated, msg)
	if err == nil {
		// Protokoll klein halten.
		_, err = s.db.Exec(`DELETE FROM fetch_log WHERE id NOT IN (SELECT id FROM fetch_log ORDER BY id DESC LIMIT 200)`)
	}
	return err
}

func (s *Store) RecentFetches(n int) ([]FetchResult, error) {
	rows, err := s.db.Query(`SELECT started_at, finished_at, messages, listings_new, listings_upd, error FROM fetch_log ORDER BY id DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FetchResult
	for rows.Next() {
		var r FetchResult
		var st, fi int64
		if err := rows.Scan(&st, &fi, &r.Messages, &r.New, &r.Updated, &r.ErrText); err != nil {
			return nil, err
		}
		r.Started, r.Finished = time.Unix(st, 0), time.Unix(fi, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
