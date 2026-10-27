package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Die Alert-Mails der Portale ändern ihr Layout gelegentlich. Der Parser verlässt sich deshalb
// nicht auf feste HTML-Strukturen, sondern auf zwei robuste Merkmale:
//  1. Links auf Exposés (auch hinter Tracking-Weiterleitungen, solange die Ziel-URL darin steckt),
//  2. Zahlen mit Einheit (€, m², Zimmer) im Text zwischen zwei Exposés.

const (
	linkStart = '\x01'
	linkEnd   = '\x02'
)

type exposePattern struct {
	source string
	re     *regexp.Regexp
	url    func(id string) string
}

var exposePatterns = []exposePattern{
	{"is24", regexp.MustCompile(`immobilienscout24\.de/expose/(\d{5,})`),
		func(id string) string { return "https://www.immobilienscout24.de/expose/" + id }},
	{"immowelt", regexp.MustCompile(`immowelt\.de/(?:projekte/)?expose/([A-Za-z0-9-]{5,})`),
		func(id string) string { return "https://www.immowelt.de/expose/" + id }},
	{"kleinanzeigen", regexp.MustCompile(`kleinanzeigen\.de/s-anzeige/[^/?#\s]+/(\d{5,})`),
		func(id string) string { return "https://www.kleinanzeigen.de/s-anzeige/" + id }},
}

// Absender-Domains, für die Inserate ohne erkennbare Exposé-ID per Hash erkannt werden.
var sourceDomains = map[string]string{
	"immobilienscout24.de": "is24",
	"immowelt.de":          "immowelt",
	"kleinanzeigen.de":     "kleinanzeigen",
}

// exposeFromURL findet Quelle und Exposé-ID in einer (evtl. weitergeleiteten) URL.
func exposeFromURL(href string) (source, id, canonical string, ok bool) {
	u := href
	for i := 0; i < 4; i++ {
		for _, p := range exposePatterns {
			if m := p.re.FindStringSubmatch(u); m != nil {
				return p.source, m[1], p.url(m[1]), true
			}
		}
		dec, err := url.QueryUnescape(u)
		if err != nil || dec == u {
			break
		}
		u = dec
	}
	return "", "", "", false
}

// ---- HTML → Text mit Link-Markern ----

var (
	reDropBlocks = regexp.MustCompile(`(?is)<(head|style|script|title)\b.*?</(head|style|script|title)\s*>`)
	reComment    = regexp.MustCompile(`(?s)<!--.*?-->`)
	reAnchor     = regexp.MustCompile(`(?is)<a\b[^>]*?\bhref\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)[^>]*>`)
	reBlockTag   = regexp.MustCompile(`(?i)</?(br|p|div|tr|td|th|li|ul|ol|table|tbody|h[1-6]|section|article|header|footer|center)\b[^>]*>`)
	reAnyTag     = regexp.MustCompile(`(?s)<[^>]*>`)
	reBareURL    = regexp.MustCompile(`https?://[^\s<>"'\x01\x02]+`)
	reSpaces     = regexp.MustCompile(`[ \t\x{00A0}\x{2009}\x{202F}]+`)
)

func htmlToText(s string) string {
	s = reComment.ReplaceAllString(s, "")
	s = reDropBlocks.ReplaceAllString(s, "")
	s = reAnchor.ReplaceAllStringFunc(s, func(tag string) string {
		m := reAnchor.FindStringSubmatch(tag)
		href := strings.Trim(m[1], `"'`)
		return " " + string(linkStart) + html.UnescapeString(href) + string(linkEnd)
	})
	s = reBlockTag.ReplaceAllString(s, "\n")
	s = reAnyTag.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return cleanLines(s)
}

func plainToText(s string) string {
	s = reBareURL.ReplaceAllStringFunc(s, func(u string) string {
		return string(linkStart) + strings.TrimRight(u, ".,;)>]") + string(linkEnd)
	})
	return cleanLines(s)
}

func cleanLines(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(reSpaces.ReplaceAllString(line, " "))
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// ---- Zahlen ----

var (
	reEuro        = regexp.MustCompile(`(?i)(?:€|eur(?:o)?)\s*(\d{1,3}(?:[.\s]\d{3})+|\d+)(?:,(?:\d{1,2}|-{1,2}))?|(\d{1,3}(?:[.\s]\d{3})+|\d+)(?:,(?:\d{1,2}|-{1,2}))?\s*(?:€|eur(?:o)?\b)`)
	reArea        = regexp.MustCompile(`(?i)(\d{1,4}(?:,\d{1,2})?)\s*(?:m²|m2\b|qm\b|m\^2)`)
	reRooms       = regexp.MustCompile(`(?i)(\d{1,2}(?:,\d)?|\d½)\s*(?:zi\b\.?|zimmer\b|zi\.|räume\b)`)
	rePLZCity     = regexp.MustCompile(`\b(\d{5})\s+([A-ZÄÖÜ][^\x01\x02]*)`)
	reFooter      = regexp.MustCompile(`(?i)impressum|abmelden|abbestellen|datenschutz|suchauftrag (?:bearbeiten|löschen|verwalten|ändern)|newsletter|handelsregister|geschäftsführ|amtsgericht`)
	reBoilerplate = regexp.MustCompile(`(?i)suchauftrag|neues? (?:angebot|immobilien|inserat|treffer|objekt)|angebote? für|treffer zu|ihre suche|deine suche|deiner (?:gespeicherten )?suche|passend zu|für dich|weitere immobilien|im projekt|immobilie wert|preise ansehen|karte anzeigen|inserieren|hol dir die app|käufer gesucht|hallo|guten tag|sehr geehrte`)
	reButtonish   = regexp.MustCompile(`(?i)^(zum |zur |details|ansehen|mehr|exposé|expose|angebot ansehen|jetzt |\d+ bilder?|merken|anzeigen)`)
	// Anbieterzeilen: Maklerfirmen, Banken, Bauträger – weder Titel noch Ort.
	reProvider = regexp.MustCompile(`(?i)\b(gmbh|mbh|ag|kg|ug|e\.\s?k\.|eg|immobilien|immobilienwelt|makler|maklerbüro|sparkasse|volksbank|raiffeisenbank|re/max|remax|engel & völkers|von poll|homeday|bauträger|wohnbau|gewerbebau|hausverwaltung|vertriebs?)\b`)
	rePerM2    = regexp.MustCompile(`(?i)(€|eur)\s*(/|pro)\s*(m²|m2|qm)`)
	rePLZParen = regexp.MustCompile(`\((\d{5})\)`)
)

// parseGermanInt liest „289.000“ oder „289 000“ als 289000.
func parseGermanInt(s string) int64 {
	s = strings.NewReplacer(".", "", " ", "").Replace(s)
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func parseGermanFloat(s string) float64 {
	s = strings.ReplaceAll(s, "½", ",5")
	s = strings.ReplaceAll(strings.ReplaceAll(s, ".", ""), ",", ".")
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func euroAmounts(line string) []int64 {
	var out []int64
	for _, m := range reEuro.FindAllStringSubmatchIndex(line, -1) {
		// „3.200 €/m²“ ist ein Quadratmeterpreis, kein Kaufpreis.
		rest := line[m[1]:]
		if r := strings.TrimLeft(rest, " "); strings.HasPrefix(r, "/m") || strings.HasPrefix(strings.ToLower(r), "pro m") {
			continue
		}
		g := 2
		if m[4] >= 0 {
			g = 4
		}
		out = append(out, parseGermanInt(line[m[g]:m[g+1]]))
	}
	return out
}

// ---- Inserate aus einer Mail ----

type ParsedListing struct {
	Listing
	Hashed bool // keine Exposé-ID gefunden, ID aus Titel und Fläche gebildet
}

type marker struct {
	start, end int // Position des Markers im Text
	source, id string
	canonical  string
	href       string
}

// parseListings extrahiert alle Inserate aus einer dekodierten Alert-Mail.
func parseListings(msg Message) []ParsedListing {
	var text string
	if msg.HTML != "" {
		text = htmlToText(msg.HTML)
	} else {
		text = plainToText(msg.Text)
	}
	fallbackSource := ""
	for dom, src := range sourceDomains {
		if strings.Contains(strings.ToLower(msg.From), dom) {
			fallbackSource = src
		}
	}

	var marks []marker
	for i := 0; i < len(text); {
		s := strings.IndexByte(text[i:], linkStart)
		if s < 0 {
			break
		}
		s += i
		e := strings.IndexByte(text[s:], linkEnd)
		if e < 0 {
			break
		}
		e += s
		href := text[s+1 : e]
		if src, id, canon, ok := exposeFromURL(href); ok {
			marks = append(marks, marker{start: s, end: e + 1, source: src, id: id, canonical: canon, href: href})
		}
		i = e + 1
	}

	var out []ParsedListing
	if len(marks) == 0 {
		if fallbackSource != "" {
			out = parseWithoutIDs(text, fallbackSource)
		}
		return out
	}

	// Aufeinanderfolgende Marker mit derselben ID gehören zum selben Inserat (Bild, Titel, Button).
	type group struct {
		m                  marker
		firstAt, lastEnd   int // erster Marker, Ende des letzten Markers
		blockFrom, blockTo int
	}
	var groups []group
	for _, m := range marks {
		if n := len(groups); n > 0 && groups[n-1].m.id == m.id && groups[n-1].m.source == m.source {
			groups[n-1].lastEnd = m.end
			continue
		}
		groups = append(groups, group{m: m, firstAt: m.start, lastEnd: m.end})
	}
	// Zwei Layouts: Link zuerst, dann Details (HTML-Mails) – oder Details, dann Link (Text-Mails).
	// Stehen schon vor dem ersten Link Kaufpreise, gehört der Text vor einem Link zu diesem Inserat.
	detailsFirst := false
	for _, a := range euroAmounts(strings.ReplaceAll(text[:groups[0].firstAt], "\n", " ")) {
		if a >= 10000 {
			detailsFirst = true
		}
	}
	for i := range groups {
		switch {
		case detailsFirst && i == 0:
			groups[i].blockFrom, groups[i].blockTo = 0, groups[i].lastEnd
		case detailsFirst:
			groups[i].blockFrom, groups[i].blockTo = groups[i-1].lastEnd, groups[i].lastEnd
		case i+1 < len(groups):
			groups[i].blockFrom, groups[i].blockTo = groups[i].firstAt, groups[i+1].firstAt
		default:
			groups[i].blockFrom, groups[i].blockTo = groups[i].firstAt, len(text)
		}
	}
	seen := map[string]bool{}
	for _, g := range groups {
		key := g.m.source + "/" + g.m.id
		if seen[key] {
			continue
		}
		seen[key] = true
		l := extractFields(text[g.blockFrom:g.blockTo])
		l.Source, l.ExternalID, l.URL = g.m.source, g.m.id, g.m.canonical
		out = append(out, ParsedListing{Listing: l})
	}
	return out
}

// blockLines liefert die Textzeilen eines Inserat-Blocks ohne Link-Marker,
// abgeschnitten am Mail-Footer.
func blockLines(block string) []string {
	var lines []string
	for _, raw := range strings.Split(block, "\n") {
		line := raw
		for {
			s := strings.IndexByte(line, linkStart)
			if s < 0 {
				break
			}
			e := strings.IndexByte(line[s:], linkEnd)
			if e < 0 {
				line = line[:s]
				break
			}
			line = line[:s] + line[s+e+1:]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if reFooter.MatchString(line) {
			break
		}
		lines = append(lines, line)
		if len(lines) >= 15 {
			break
		}
	}
	return lines
}

func extractFields(block string) Listing {
	var l Listing
	lines := blockLines(block)
	var maxPrice int64
	for _, line := range lines {
		low := strings.ToLower(line)
		amounts := euroAmounts(line)
		isRent := strings.Contains(low, "miete") || strings.Contains(low, "vermietet") || strings.Contains(low, "rendite")
		for _, a := range amounts {
			switch {
			case isRent && a > 0 && a < 10000:
				if l.RentActual == 0 && strings.Contains(low, "miete") {
					if strings.Contains(low, "jahr") || strings.Contains(low, "p.a") || strings.Contains(low, "jährlich") {
						l.RentActual = (a + 6) / 12
					} else {
						l.RentActual = a
					}
				}
			case isRent && a >= 10000 && (strings.Contains(low, "jahr") || strings.Contains(low, "p.a")):
				if l.RentActual == 0 {
					l.RentActual = (a + 6) / 12
				}
			case a >= 10000 && a <= 50_000_000:
				if strings.Contains(low, "kaufpreis") {
					l.Price = a
				} else if a > maxPrice {
					maxPrice = a
				}
			}
		}
		if l.Area == 0 {
			for _, m := range reArea.FindAllStringSubmatchIndex(line, -1) {
				before := strings.ToLower(line[:m[0]])
				if strings.HasSuffix(strings.TrimSpace(before), "€/") || strings.Contains(before, "grundstück") && !strings.Contains(before, "wohnfläche") {
					continue
				}
				if f := parseGermanFloat(line[m[2]:m[3]]); f >= 10 && f <= 2000 {
					l.Area = f
					break
				}
			}
		}
		if l.Rooms == 0 {
			if m := reRooms.FindStringSubmatch(line); m != nil {
				if f := parseGermanFloat(m[1]); f > 0 && f <= 30 {
					l.Rooms = f
				}
			}
		}
		if l.Postcode == "" {
			if m := rePLZCity.FindStringSubmatch(line); m != nil && len(amounts) == 0 {
				l.Postcode = m[1]
				l.Address = strings.TrimSpace(line)
			}
		}
	}
	if l.Price == 0 {
		l.Price = maxPrice
	}
	// Titel ist die erste Textzeile, danach folgt die Ortsangabe – bei immowelt über
	// mehrere Zeilen verteilt („Ingolstadt,“ / „Ingolstadt“ / „(85055)“).
	var place []string
	for _, line := range lines {
		if reButtonish.MatchString(line) || reBoilerplate.MatchString(line) {
			if len(place) > 0 {
				break
			}
			continue
		}
		if len(euroAmounts(line)) > 0 || reArea.MatchString(line) || reRooms.MatchString(line) || rePerM2.MatchString(line) || reProvider.MatchString(line) {
			continue
		}
		if line == l.Address || l.Title == "" && len([]rune(line)) < 8 {
			continue
		}
		if l.Title == "" {
			l.Title = truncate(line, 200)
			continue
		}
		if l.Address != "" || len([]rune(line)) > 80 {
			break
		}
		place = append(place, line)
		if len(place) == 4 {
			break
		}
	}
	if l.Address == "" && len(place) > 0 {
		l.Address = strings.TrimSpace(strings.ReplaceAll(strings.Join(place, " "), " ,", ","))
		if m := rePLZParen.FindStringSubmatch(l.Address); m != nil {
			l.Postcode = m[1]
		} else if m := plzRe.FindString(l.Address); m != "" {
			l.Postcode = m
		}
	}
	return l
}

// parseWithoutIDs ist der Weg für Mails, deren Links keine Exposé-URL enthalten (immowelt
// verschlüsselt alle Links über click.by.immowelt.de). Ein Inserat beginnt mit seiner
// Kaufpreis-Zeile und reicht bis zur nächsten; die ID ist ein Hash aus Titel und Fläche,
// der Link die Weiterleitung am Titel.
func parseWithoutIDs(text, source string) []ParsedListing {
	lines := strings.Split(text, "\n")
	var starts []int
	for i, line := range lines {
		low := strings.ToLower(stripMarkers(line))
		if strings.Contains(low, "miete") {
			continue
		}
		for _, a := range euroAmounts(stripMarkers(line)) {
			if a >= 10000 {
				starts = append(starts, i)
				break
			}
		}
	}
	var out []ParsedListing
	seen := map[string]bool{}
	for n, from := range starts {
		to := len(lines)
		if n+1 < len(starts) {
			to = starts[n+1]
		}
		block := strings.Join(lines[from:to], "\n")
		l := extractFields(block)
		if l.Title == "" {
			// Layout mit Titel vor dem Preis: ein paar Zeilen zurückschauen.
			block = strings.Join(lines[max(0, from-4):to], "\n")
			l = extractFields(block)
		}
		if l.Price == 0 || l.Area == 0 || l.Title == "" {
			continue
		}
		h := sha256.Sum256([]byte(strings.ToLower(l.Title) + "|" + strconv.FormatFloat(l.Area, 'f', 1, 64)))
		l.Source, l.ExternalID = source, "h-"+hex.EncodeToString(h[:8])
		if seen[l.ExternalID] {
			continue
		}
		seen[l.ExternalID] = true
		l.URL = titleLink(block, l.Title)
		out = append(out, ParsedListing{Listing: l, Hashed: true})
	}
	return out
}

// titleLink liefert den Link der Zeile mit dem Titel, sonst den ersten Link im Block.
func titleLink(block, title string) string {
	first := ""
	for _, line := range strings.Split(block, "\n") {
		s := strings.IndexByte(line, linkStart)
		if s < 0 {
			continue
		}
		e := strings.IndexByte(line[s:], linkEnd)
		if e < 0 {
			continue
		}
		href := line[s+1 : s+e]
		if !strings.HasPrefix(href, "https://") && !strings.HasPrefix(href, "http://") {
			continue
		}
		if strings.Contains(line, title) {
			return href
		}
		if first == "" {
			first = href
		}
	}
	return first
}

func stripMarkers(line string) string {
	for {
		s := strings.IndexByte(line, linkStart)
		if s < 0 {
			return line
		}
		e := strings.IndexByte(line[s:], linkEnd)
		if e < 0 {
			return line[:s]
		}
		line = line[:s] + line[s+e+1:]
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// ---- CLI ----

func parseCmd(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	msg, err := decodeMessage(raw)
	if err != nil {
		return err
	}
	fmt.Printf("Betreff: %s\nVon:     %s\n\n", msg.Subject, msg.From)
	ls := parseListings(msg)
	for i, l := range ls {
		fmt.Printf("%2d. [%s %s]%s %s\n    %s | PLZ %s\n    Preis %d € · %.1f m² · %.1f Zi.",
			i+1, l.Source, l.ExternalID, map[bool]string{true: " (Hash-ID)"}[l.Hashed], l.Title, l.Address, l.Postcode, l.Price, l.Area, l.Rooms)
		if l.RentActual > 0 {
			fmt.Printf(" · Ist-Miete %d €", l.RentActual)
		}
		fmt.Printf("\n    %s\n", l.URL)
	}
	fmt.Printf("\n%d Inserate erkannt\n", len(ls))
	return nil
}

func importFile(store *Store, path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	n, _, err := importMessage(store, raw)
	return n, err
}

// importMessage speichert alle Inserate einer Mail. Rückgabe: Anzahl, davon neu.
func importMessage(store *Store, raw []byte) (total, fresh int, err error) {
	msg, err := decodeMessage(raw)
	if err != nil {
		return 0, 0, err
	}
	for _, p := range parseListings(msg) {
		isNew, err := store.UpsertListing(p.Listing)
		if err != nil {
			return total, fresh, err
		}
		total++
		if isNew {
			fresh++
		}
	}
	return total, fresh, nil
}
