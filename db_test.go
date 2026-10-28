package main

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := openDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Store{db: db}
}

func TestUpsertListingKeepsFirstSeenAndTracksPrice(t *testing.T) {
	s := newTestStore(t)
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return t0 }
	isNew, err := s.UpsertListing(Listing{Source: "is24", ExternalID: "1", URL: "u", Title: "A", Price: 150000, Area: 50})
	if err != nil || !isNew {
		t.Fatalf("neu: %v %v", isNew, err)
	}
	s.now = func() time.Time { return t0.Add(72 * time.Hour) }
	isNew, err = s.UpsertListing(Listing{Source: "is24", ExternalID: "1", Price: 139000, Rooms: 2, Title: ""})
	if err != nil || isNew {
		t.Fatalf("Update: %v %v", isNew, err)
	}
	ls, _ := s.Listings()
	if len(ls) != 1 {
		t.Fatalf("%d Inserate", len(ls))
	}
	l := ls[0]
	if l.Price != 139000 || l.FirstPrice != 150000 || l.Title != "A" || l.Rooms != 2 || l.Area != 50 {
		t.Errorf("%+v", l)
	}
	if !l.FirstSeen.Equal(t0) || !l.LastSeen.Equal(t0.Add(72*time.Hour)) {
		t.Errorf("Zeitpunkte: %v / %v", l.FirstSeen, l.LastSeen)
	}
}

func TestCitiesCRUD(t *testing.T) {
	s := newTestStore(t)
	c := City{Name: "Ansbach", Postcodes: []string{"91522"}, RentPerM2: 8.9, Active: true}
	if err := s.SaveCity(&c); err != nil || c.ID == 0 {
		t.Fatal(c.ID, err)
	}
	dup := City{Name: "ansbach"}
	if err := s.SaveCity(&dup); err != errDuplicateCity {
		t.Errorf("Dublette: %v", err)
	}
	c.RentPerM2 = 0
	if err := s.SaveCity(&c); err != nil {
		t.Fatal(err)
	}
	got, _ := s.City(c.ID)
	if got.RentPerM2 != 0 || got.Postcodes[0] != "91522" {
		t.Errorf("%+v", got)
	}
	if err := s.DeleteCity(c.ID); err != nil {
		t.Fatal(err)
	}
	if cs, _ := s.Cities(); len(cs) != 0 {
		t.Error("nicht gelöscht")
	}
}

func TestThresholdDefault(t *testing.T) {
	s := newTestStore(t)
	if s.Threshold() != 5 {
		t.Errorf("Standard: %v", s.Threshold())
	}
	s.SetThreshold(4.5)
	if s.Threshold() != 4.5 {
		t.Errorf("gespeichert: %v", s.Threshold())
	}
}

func TestResetImported(t *testing.T) {
	s := newTestStore(t)
	s.UpsertListing(Listing{Source: "immowelt", ExternalID: "h-1"})
	s.UpsertListing(Listing{Source: "manual", ExternalID: "m"})
	s.UpsertListing(Listing{Source: "is24", ExternalID: "2"})
	ls, _ := s.Listings()
	for _, l := range ls {
		if l.ExternalID == "2" {
			l.Note = "anschauen"
			s.UpdateListingManual(l)
		}
	}
	s.SetMailState("INBOX", 3, 99)
	n, err := s.ResetImported()
	if err != nil || n != 1 {
		t.Fatalf("%d gelöscht, %v", n, err)
	}
	if ls, _ := s.Listings(); len(ls) != 2 {
		t.Errorf("%d übrig", len(ls))
	}
	if v, last, _ := s.MailState("INBOX"); v != 0 || last != 0 {
		t.Errorf("IMAP-Stand nicht zurückgesetzt: %d %d", v, last)
	}
}

func TestResetImportedKeepsFavorites(t *testing.T) {
	s := newTestStore(t)
	s.UpsertListing(Listing{Source: "immowelt", ExternalID: "h-1"})
	s.UpsertListing(Listing{Source: "immowelt", ExternalID: "h-2"})
	ls, _ := s.Listings()
	s.SetFavorite(ls[0].ID, true)
	if n, _ := s.ResetImported(); n != 1 {
		t.Errorf("%d gelöscht", n)
	}
	ls, _ = s.Listings()
	if len(ls) != 1 || !ls[0].Favorite {
		t.Errorf("Favorit nicht erhalten: %+v", ls)
	}
}

func TestMigrationMarksOldListingsRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alt.db")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	// Stand vor Migration 3 simulieren.
	for _, q := range []string{`DROP INDEX listings_unread`, `ALTER TABLE listings DROP COLUMN read_at`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Exec(`PRAGMA user_version = 2`)
	now := time.Now().Unix()
	db.Exec(`INSERT INTO listings (source, external_id, url, first_seen, last_seen) VALUES ('is24','alt','',?,?), ('is24','frisch','',?,?)`, now-5*86400, now, now-3600, now)
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	s := &Store{db: db}
	ls, _ := s.Listings()
	read := map[string]bool{}
	for _, l := range ls {
		read[l.ExternalID] = l.Read
	}
	if !read["alt"] || read["frisch"] {
		t.Errorf("%v", read)
	}
	db.Close()
}
