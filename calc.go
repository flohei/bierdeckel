package main

import (
	"regexp"
	"sort"
	"strings"
	"time"
)

// RentSource sagt, woher die angesetzte Miete stammt.
type RentSource int

const (
	RentNone   RentSource = iota // keine Miete bekannt → keine Rendite
	RentIndex                    // Fläche × Mietspiegel der Stadt
	RentActual                   // Ist-Miete aus dem Inserat oder manuell eingetragen
)

// Row ist ein Inserat mit Bierdeckelrechnung, wie es im Dashboard erscheint.
type Row struct {
	Listing
	City        *City
	MonthlyRent float64 // nettokalt €/Monat
	RentSource  RentSource
	Yield       float64 // Bruttomietrendite in Prozent; 0 = nicht berechenbar
	Factor      float64 // Kaufpreisfaktor (Kaufpreis / Jahresmiete)
	PricePerM2  float64
	New         bool
}

// Bierdeckelrechnung: Bruttomietrendite = Jahresnettokaltmiete / Kaufpreis.
func grossYield(price int64, monthlyRent float64) (yield, factor float64) {
	if price <= 0 || monthlyRent <= 0 {
		return 0, 0
	}
	annual := monthlyRent * 12
	return annual / float64(price) * 100, float64(price) / annual
}

func evaluate(l Listing, cities []City, now time.Time) Row {
	r := Row{Listing: l, City: matchCity(l, cities)}
	if l.Area > 0 && l.Price > 0 {
		r.PricePerM2 = float64(l.Price) / l.Area
	}
	switch {
	case l.RentActual > 0:
		r.MonthlyRent, r.RentSource = float64(l.RentActual), RentActual
	case r.City != nil && r.City.RentPerM2 > 0 && l.Area > 0:
		r.MonthlyRent, r.RentSource = l.Area*r.City.RentPerM2, RentIndex
	}
	r.Yield, r.Factor = grossYield(l.Price, r.MonthlyRent)
	r.New = now.Sub(l.FirstSeen) < 48*time.Hour
	return r
}

var plzRe = regexp.MustCompile(`\b\d{5}\b`)

// matchCity ordnet ein Inserat einer Stadt aus der Liste zu: zuerst über die Postleitzahl,
// sonst über den Ortsnamen als ganzes Wort in Adresse oder Titel.
func matchCity(l Listing, cities []City) *City {
	pcs := map[string]bool{}
	if l.Postcode != "" {
		pcs[l.Postcode] = true
	}
	for _, p := range plzRe.FindAllString(l.Address, -1) {
		pcs[p] = true
	}
	for i := range cities {
		for _, p := range cities[i].Postcodes {
			if pcs[p] {
				return &cities[i]
			}
		}
	}
	hay := " " + normalizeWords(l.Address) + " "
	title := " " + normalizeWords(l.Title) + " "
	var best *City
	for i := range cities {
		needle := " " + normalizeWords(cities[i].Name) + " "
		if strings.Contains(hay, needle) {
			// Längster Treffer gewinnt („Neustadt an der Aisch“ vor „Neustadt“).
			if best == nil || len(cities[i].Name) > len(best.Name) {
				best = &cities[i]
			}
		}
	}
	if best != nil {
		return best
	}
	for i := range cities {
		if strings.Contains(title, " "+normalizeWords(cities[i].Name)+" ") {
			if best == nil || len(cities[i].Name) > len(best.Name) {
				best = &cities[i]
			}
		}
	}
	return best
}

func normalizeWords(s string) string {
	s = strings.ToLower(s)
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == 'ä' || r == 'ö' || r == 'ü' || r == 'ß')
	}), " ")
}

// Filter steuert, welche Zeilen das Dashboard zeigt.
type Filter struct {
	Threshold  float64 // in Prozent
	All        bool    // auch unter dem Schwellwert und ohne Rendite
	ShowHidden bool
	OnlyActive bool  // Inserate aus inaktiven Städten ausblenden
	CityID     int64 // 0 = alle
	Favorites  bool  // nur Favoriten (unabhängig von Schwellwert, Ausblenden und inaktiven Städten)
	Sort       string
}

var sortKeys = map[string]string{
	"rendite": "Rendite",
	"neu":     "Neueste",
	"preis":   "Preis",
	"qm":      "Preis/m²",
}

func buildRows(listings []Listing, cities []City, f Filter, now time.Time) []Row {
	var rows []Row
	for _, l := range listings {
		if f.Favorites && !l.Favorite {
			continue
		}
		if l.Hidden && !f.ShowHidden && !f.Favorites {
			continue
		}
		r := evaluate(l, cities, now)
		if f.CityID != 0 && (r.City == nil || r.City.ID != f.CityID) {
			continue
		}
		if !f.Favorites && f.OnlyActive && r.City != nil && !r.City.Active {
			continue
		}
		// Favoriten bleiben sichtbar, auch wenn sie (z. B. nach Änderung des Mietspiegels) unter den Schwellwert fallen.
		if !f.All && !l.Favorite && r.Yield < f.Threshold {
			continue
		}
		rows = append(rows, r)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		switch f.Sort {
		case "neu":
			return a.FirstSeen.After(b.FirstSeen)
		case "preis":
			return a.Price < b.Price
		case "qm":
			if (a.PricePerM2 == 0) != (b.PricePerM2 == 0) {
				return b.PricePerM2 == 0
			}
			return a.PricePerM2 < b.PricePerM2
		default:
			return a.Yield > b.Yield
		}
	})
	return rows
}
