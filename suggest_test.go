package main

import (
	"reflect"
	"testing"
)

func TestPlaceName(t *testing.T) {
	cases := []struct {
		l        Listing
		name, pc string
	}{
		{Listing{Address: "Brückencenter 3, 91522 Ansbach"}, "Ansbach", "91522"},
		{Listing{Address: "91567 Herrieden"}, "Herrieden", "91567"},
		{Listing{Address: "Ansbach (Eyb)"}, "Ansbach", ""},
		{Listing{Address: "Eyb, Ansbach", Postcode: "91522"}, "Ansbach", "91522"},
		{Listing{Address: "91550 Dinkelsbühl - Altstadt"}, "Dinkelsbühl", "91550"},
		{Listing{Address: "91541 Rothenburg ob der Tauber"}, "Rothenburg ob der Tauber", "91541"},
		{Listing{Address: "Schalkhäuser Str."}, "", ""},
		{Listing{Address: ""}, "", ""},
	}
	for _, c := range cases {
		name, pc := placeName(c.l)
		if name != c.name || pc != c.pc {
			t.Errorf("%q: %q %q, erwartet %q %q", c.l.Address, name, pc, c.name, c.pc)
		}
	}
}

func TestSuggestCities(t *testing.T) {
	cities := []City{{ID: 1, Name: "Ansbach", Postcodes: []string{"91522"}}}
	ls := []Listing{
		{Address: "Hauptstr. 1, 91522 Ansbach"},     // schon zugeordnet
		{Address: "91567 Herrieden"},                // Vorschlag
		{Address: "Bahnhofstr. 2, 91567 Herrieden"}, // gleiche Stadt
		{Address: "91567 HERRIEDEN"},                // gleiche Stadt, andere Schreibweise
		{Address: "91555 Feuchtwangen"},
		{Address: "91555 Feuchtwangen", Hidden: true}, // ausgeblendet zählt nicht
		{Address: "Lichtenau (Mittelfranken)", Postcode: "91586"},
	}
	got := suggestCities(ls, cities)
	want := []CitySuggestion{
		{Name: "Herrieden", Postcodes: []string{"91567"}, Count: 3},
		{Name: "Feuchtwangen", Postcodes: []string{"91555"}, Count: 1},
		{Name: "Lichtenau", Postcodes: []string{"91586"}, Count: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("\n%+v\nerwartet\n%+v", got, want)
	}
}
