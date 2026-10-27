package main

import (
	"os"
	"strings"
	"testing"
)

// Die Mails in testdata/ sind synthetisch nachgebaut. Echte Alert-Mails (anonymisiert)
// sollten als zusätzliche Fixtures dazukommen, sobald sie vorliegen.

func mustParseFile(t *testing.T, path string) []ParsedListing {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := decodeMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parseListings(msg)
}

func TestParseIS24(t *testing.T) {
	ls := mustParseFile(t, "testdata/is24-synthetisch.eml")
	if len(ls) != 2 {
		t.Fatalf("2 Inserate erwartet, %d gefunden: %+v", len(ls), ls)
	}
	a, b := ls[0], ls[1]
	if a.Source != "is24" || a.ExternalID != "151234567" || a.URL != "https://www.immobilienscout24.de/expose/151234567" {
		t.Errorf("Kennung: %+v", a.Listing)
	}
	if a.Price != 149000 || a.Area != 52.3 || a.Rooms != 2 || a.Postcode != "91522" {
		t.Errorf("Werte: %+v", a.Listing)
	}
	if a.Title != "Gepflegte 2-Zimmer-Wohnung mit Balkon in ruhiger Lage" {
		t.Errorf("Titel: %q", a.Title)
	}
	if a.RentActual != 0 {
		t.Errorf("keine Ist-Miete erwartet: %d", a.RentActual)
	}
	if b.RentActual != 390 || b.Price != 89500 || b.Area != 31 {
		t.Errorf("zweites Inserat: %+v", b.Listing)
	}
	// Die Firmenadresse im Footer darf nicht als Ort durchrutschen.
	if b.Postcode == "10115" {
		t.Error("Footer-Adresse als PLZ erkannt")
	}
}

func TestParseImmoweltRedirectsAndCP1252(t *testing.T) {
	ls := mustParseFile(t, "testdata/immowelt-synthetisch.eml")
	if len(ls) != 2 {
		t.Fatalf("2 Inserate erwartet, %d gefunden", len(ls))
	}
	if ls[0].ExternalID != "2b7kq5x" || ls[0].Price != 219000 || ls[0].Area != 74 || ls[0].Rooms != 3 {
		t.Errorf("erstes Inserat: %+v", ls[0].Listing)
	}
	if ls[0].Address != "Ansbach (Eyb)" {
		t.Errorf("Ort: %q", ls[0].Address)
	}
	if ls[1].Price != 185000 || ls[1].Area != 61.5 || ls[1].Rooms != 2.5 || ls[1].Postcode != "91567" {
		t.Errorf("zweites Inserat: %+v", ls[1].Listing)
	}
}

func TestParsePlainTextDetailsBeforeLink(t *testing.T) {
	ls := mustParseFile(t, "testdata/text-synthetisch.eml")
	if len(ls) != 1 {
		t.Fatalf("1 Inserat erwartet, %d gefunden", len(ls))
	}
	l := ls[0]
	if l.Title != "Sanierte Altbauwohnung im Zentrum" || l.Price != 199000 || l.Area != 68 || l.Postcode != "91522" {
		t.Errorf("%+v", l.Listing)
	}
}

func TestEuroAmounts(t *testing.T) {
	cases := map[string][]int64{
		"149.000 €":                  {149000},
		"€ 185.000,-":                {185000},
		"Kaufpreis: 199.000 EUR":     {199000},
		"219 000 Euro":               {219000},
		"2.959 €/m²":                 nil,
		"3.100 € pro m²":             nil,
		"Ist-Miete 390 € kalt":       {390},
		"1.250,50 € und 98.000 €":    {1250, 98000},
		"Hausgeld 210 €, 120.000 €":  {210, 120000},
		"keine Zahlen hier":          nil,
		"Baujahr 1995, 3 Zimmer":     nil,
		"EUR 75.000":                 {75000},
		"Preis auf Anfrage":          nil,
		"64,5 m² für 129.900,00 €":   {129900},
		"Eurovision 2026":            nil,
		"Mieteinnahmen 7.800 €/Jahr": {7800},
	}
	for in, want := range cases {
		got := euroAmounts(in)
		if len(got) != len(want) {
			t.Errorf("%q: %v, erwartet %v", in, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("%q: %v, erwartet %v", in, got, want)
			}
		}
	}
}

func TestRentPerYear(t *testing.T) {
	l := extractFields("Apartment Innenstadt\n95.000 €\n30 m²\nMieteinnahmen 4.200 € pro Jahr")
	if l.RentActual != 350 {
		t.Errorf("Ist-Miete pro Monat: %d", l.RentActual)
	}
	if l.Price != 95000 {
		t.Errorf("Preis: %d", l.Price)
	}
}

func TestExposeFromURL(t *testing.T) {
	cases := []struct{ in, src, id string }{
		{"https://www.immobilienscout24.de/expose/151234567?referrer=x", "is24", "151234567"},
		{"https://click.example.com/?u=https%253A%252F%252Fwww.immobilienscout24.de%252Fexpose%252F123456789", "is24", "123456789"},
		{"https://www.immowelt.de/expose/2b7kq5x", "immowelt", "2b7kq5x"},
		{"https://www.immowelt.de/projekte/expose/k3j2h", "immowelt", "k3j2h"},
		{"https://www.kleinanzeigen.de/s-anzeige/wohnung-ansbach/2876543210-196-1234", "kleinanzeigen", "2876543210"},
	}
	for _, c := range cases {
		src, id, _, ok := exposeFromURL(c.in)
		if !ok || src != c.src || id != c.id {
			t.Errorf("%s: %s %s %v", c.in, src, id, ok)
		}
	}
	if _, _, _, ok := exposeFromURL("https://www.immobilienscout24.de/impressum"); ok {
		t.Error("Impressum als Exposé erkannt")
	}
}

func TestParseWithoutIDsIsStable(t *testing.T) {
	msg := Message{From: "ImmoScout24 <noreply@immobilienscout24.de>", HTML: `<p><a href="https://e.immobilienscout24.de/c/AbCdEf">Ruhige Wohnung am Park</a></p>
<p>91522 Ansbach</p><p>129.000 € · 45 m² · 2 Zi.</p><p><a href="https://e.immobilienscout24.de/c/XyZ">Impressum</a></p>`}
	a := parseListings(msg)
	msg.HTML = `<p><a href="https://e.immobilienscout24.de/c/Other">Ruhige Wohnung am Park</a></p><p>91522 Ansbach</p><p>124.000 € · 45 m² · 2 Zi.</p>`
	b := parseListings(msg)
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("je 1 Inserat erwartet: %d / %d", len(a), len(b))
	}
	if !a[0].Hashed || a[0].ExternalID != b[0].ExternalID {
		t.Errorf("Hash-ID nicht stabil: %s / %s", a[0].ExternalID, b[0].ExternalID)
	}
	if b[0].Price != 124000 {
		t.Errorf("Preis: %d", b[0].Price)
	}
}

func TestNoListingsInUnrelatedMail(t *testing.T) {
	msg := Message{From: "Freund <a@example.org>", Text: "Hi, kostet 150.000 € und hat 60 m². https://example.org/foo"}
	if ls := parseListings(msg); len(ls) != 0 {
		t.Errorf("keine Inserate erwartet: %+v", ls)
	}
}

// Aufbau einer echten immowelt-Alert-Mail (Okt. 2026), Links und Inhalte ersetzt:
// alle Links laufen verschlüsselt über click.by.immowelt.de, der Ort steht über drei Zeilen verteilt.
func TestParseImmoweltOct2026(t *testing.T) {
	ls := mustParseFile(t, "testdata/immowelt-2026-10.eml")
	if len(ls) != 2 {
		t.Fatalf("2 Inserate erwartet, %d gefunden: %+v", len(ls), ls)
	}
	a := ls[0]
	if !a.Hashed || a.Source != "immowelt" || a.Title != "Helle 2-Zimmerwohnung mit Balkon und Tiefgarage" {
		t.Errorf("Kennung/Titel: %+v", a.Listing)
	}
	if a.Price != 199000 || a.Area != 67 || a.Rooms != 1.5 || a.Postcode != "85055" || a.Address != "Ingolstadt, Ingolstadt (85055)" {
		t.Errorf("Werte: %+v", a.Listing)
	}
	if !strings.HasPrefix(a.URL, "https://click.by.immowelt.de/?qs=FAKE0011") {
		t.Errorf("Link sollte der am Titel sein: %s", a.URL)
	}
	if name, pc := placeName(ls[1].Listing); name != "Ansbach" || pc != "91522" {
		t.Errorf("Ort des zweiten Inserats: %q %q", name, pc)
	}
	// Footer-Adresse (AVIV, Nürnberg) darf nicht durchrutschen.
	for _, l := range ls {
		if strings.Contains(l.Address, "Nürnberg") {
			t.Errorf("Footer als Ort: %q", l.Address)
		}
	}
}

func TestProviderLinesAreNotPlaces(t *testing.T) {
	l := extractFields("Schöne 3-Zimmer-Wohnung mit Garten\nImmobilien Zentrum Vertriebs GmbH\n229.000 € · 81 m² · 3 Zi.")
	if l.Title != "Schöne 3-Zimmer-Wohnung mit Garten" || l.Address != "" {
		t.Errorf("Anbieter als Ort: %+v", l)
	}
	l = extractFields("Kapitalanlage nahe Altstadt\nSparkasse Nürnberg\nNürnberg, Gostenhof\n189.000 €")
	if l.Address != "Nürnberg, Gostenhof" {
		t.Errorf("Ort nach Anbieter: %q", l.Address)
	}
	for _, s := range []string{"Deine ersten Treffer zur Suche", "Weitere Immobilien im Projekt anzeigen", "RE/MAX in Regensburg & Ostbayern", "Rathay Immobilien eG", "Löw Immobilien GmbH"} {
		if l := extractFields("Wohnung mit Balkon und Lift\n" + s + "\n150.000 €"); l.Address != "" {
			t.Errorf("%q als Ort erkannt", s)
		}
	}
}
