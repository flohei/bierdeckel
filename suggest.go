package main

import (
	"regexp"
	"sort"
	"strings"
)

// CitySuggestion ist ein Ort aus eingelesenen Inseraten, der noch keiner Stadt zugeordnet ist.
type CitySuggestion struct {
	Name      string
	Postcodes []string
	Count     int
}

var (
	rePLZName  = regexp.MustCompile(`\b(\d{5})\s+([A-ZÄÖÜ][^,()/|·\d]*)`)
	reParens   = regexp.MustCompile(`\s*\([^)]*\)`)
	reNameTail = regexp.MustCompile(`\s+(?:-|–|OT|Ortsteil)\s.*$`)
)

// placeName leitet aus der Ortsangabe eines Inserats Stadtname und PLZ ab:
// „Brückencenter 3, 91522 Ansbach“ → Ansbach, 91522; „Ansbach (Eyb)“ → Ansbach;
// „Eyb, Ansbach“ → Ansbach (bei Komma ohne PLZ ist der letzte Teil meist die Stadt).
func placeName(l Listing) (name, postcode string) {
	addr := strings.TrimSpace(l.Address)
	if m := rePLZName.FindStringSubmatch(addr); m != nil {
		name, postcode = m[2], m[1]
	} else {
		parts := strings.Split(addr, ",")
		name = parts[len(parts)-1]
		postcode = l.Postcode
	}
	name = reParens.ReplaceAllString(name, "")
	name = reNameTail.ReplaceAllString(name, "")
	name = strings.Trim(strings.TrimSpace(name), "-–·|")
	name = strings.TrimSpace(name)
	// Straßen und Füllwörter sind keine Städte.
	low := strings.ToLower(name)
	if len([]rune(name)) < 2 || len([]rune(name)) > 40 || strings.ContainsAny(name, "0123456789") ||
		strings.HasSuffix(low, "str.") || strings.HasSuffix(low, "straße") || strings.HasSuffix(low, "strasse") ||
		strings.HasSuffix(low, "weg") || strings.HasSuffix(low, "platz") {
		return "", postcode
	}
	return name, postcode
}

// suggestCities sammelt Orte aus sichtbaren Inseraten, die keiner Stadt der Liste zugeordnet sind.
func suggestCities(listings []Listing, cities []City) []CitySuggestion {
	byKey := map[string]*CitySuggestion{}
	pcSeen := map[string]map[string]bool{}
	for _, l := range listings {
		if l.Hidden || matchCity(l, cities) != nil {
			continue
		}
		name, pc := placeName(l)
		if name == "" {
			continue
		}
		key := normalizeWords(name)
		s := byKey[key]
		if s == nil {
			s = &CitySuggestion{Name: name}
			byKey[key] = s
			pcSeen[key] = map[string]bool{}
		}
		s.Count++
		if pc != "" && !pcSeen[key][pc] {
			pcSeen[key][pc] = true
			s.Postcodes = append(s.Postcodes, pc)
		}
	}
	out := make([]CitySuggestion, 0, len(byKey))
	for _, s := range byKey {
		sort.Strings(s.Postcodes)
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}
