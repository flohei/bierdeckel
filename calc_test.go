package main

import (
	"math"
	"testing"
	"time"
)

func near(a, b float64) bool { return math.Abs(a-b) < 0.005 }

func TestGrossYield(t *testing.T) {
	y, f := grossYield(150000, 625) // 7.500 €/Jahr
	if !near(y, 5) || !near(f, 20) {
		t.Errorf("Rendite %.3f, Faktor %.3f", y, f)
	}
	if y, f := grossYield(0, 500); y != 0 || f != 0 {
		t.Error("ohne Preis keine Rendite")
	}
}

func TestEvaluateRentSources(t *testing.T) {
	cities := []City{{ID: 1, Name: "Ansbach", Postcodes: []string{"91522"}, RentPerM2: 9, Active: true}}
	now := time.Now()
	l := Listing{Price: 120000, Area: 60, Postcode: "91522", FirstSeen: now}
	r := evaluate(l, cities, now)
	if r.RentSource != RentIndex || !near(r.MonthlyRent, 540) || !near(r.Yield, 5.4) {
		t.Errorf("Mietspiegel: %+v", r)
	}
	if !r.New {
		t.Error("frisches Inserat sollte neu sein")
	}
	l.RentActual = 400
	r = evaluate(l, cities, now)
	if r.RentSource != RentActual || !near(r.Yield, 4) {
		t.Errorf("Ist-Miete hat Vorrang: %+v", r)
	}
	r = evaluate(Listing{Price: 120000, Area: 60, Address: "Irgendwo"}, cities, now)
	if r.Yield != 0 || r.City != nil {
		t.Errorf("ohne Stadt keine Rendite: %+v", r)
	}
}

func TestMatchCity(t *testing.T) {
	cities := []City{
		{ID: 1, Name: "Ansbach", Postcodes: []string{"91522"}},
		{ID: 2, Name: "Neustadt"},
		{ID: 3, Name: "Neustadt an der Aisch"},
		{ID: 4, Name: "Feuchtwangen", Postcodes: []string{"91555"}},
	}
	cases := []struct {
		l    Listing
		want int64
	}{
		{Listing{Postcode: "91522"}, 1},
		{Listing{Address: "Hauptstr. 1, 91555 Feuchtwangen"}, 4},
		{Listing{Address: "Ansbach (Eyb)"}, 1},
		{Listing{Address: "Neustadt an der Aisch, Zentrum"}, 3},
		{Listing{Address: "Neustadt"}, 2},
		{Listing{Address: "Landkreis Ansbachtal"}, 0},
		{Listing{Title: "Wohnung in Feuchtwangen-Mitte"}, 4},
		{Listing{Address: "Herrieden"}, 0},
	}
	for _, c := range cases {
		got := matchCity(c.l, cities)
		var id int64
		if got != nil {
			id = got.ID
		}
		if id != c.want {
			t.Errorf("%+v: Stadt %d, erwartet %d", c.l, id, c.want)
		}
	}
}

func TestBuildRowsFilterAndSort(t *testing.T) {
	now := time.Now()
	cities := []City{{ID: 1, Name: "Ansbach", RentPerM2: 10, Active: true}, {ID: 2, Name: "Rothenburg", RentPerM2: 10, Active: false}}
	ls := []Listing{
		{ID: 1, Address: "Ansbach", Price: 100000, Area: 50, FirstSeen: now.Add(-time.Hour)},              // 6 %
		{ID: 2, Address: "Ansbach", Price: 200000, Area: 50, FirstSeen: now},                              // 3 %
		{ID: 3, Address: "Ansbach", Price: 80000, Area: 50, FirstSeen: now.Add(-time.Hour), Hidden: true}, // 7,5 %
		{ID: 4, Address: "Rothenburg", Price: 50000, Area: 50, FirstSeen: now.Add(-time.Hour)},            // 12 %, inaktiv
		{ID: 5, Address: "Ansbach", Price: 110000, Area: 50, FirstSeen: now.Add(-2 * time.Hour)},          // 5,45 %
	}
	rows := buildRows(ls, cities, Filter{Threshold: 5, OnlyActive: true}, now)
	if len(rows) != 2 || rows[0].ID != 1 || rows[1].ID != 5 {
		t.Fatalf("Filter/Sortierung: %v", ids(rows))
	}
	rows = buildRows(ls, cities, Filter{Threshold: 5, All: true, ShowHidden: true, Sort: "neu"}, now)
	if len(rows) != 5 || rows[0].ID != 2 {
		t.Fatalf("alle, neueste zuerst: %v", ids(rows))
	}
}

func ids(rows []Row) []int64 {
	var out []int64
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}
