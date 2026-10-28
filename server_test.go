package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newTestApp(t *testing.T) (*App, http.Handler) {
	t.Helper()
	a, err := newApp(Config{User: "flo", Password: "bierdeckel", IMAPMailbox: "INBOX", Interval: time.Hour}, newTestStore(t))
	if err != nil {
		t.Fatal(err)
	}
	return a, a.routes()
}

func do(h http.Handler, method, path string, form url.Values, mod func(*http.Request)) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	r := httptest.NewRequest(method, path, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	r.SetBasicAuth("flo", "bierdeckel")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if mod != nil {
		mod(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestAuthRequired(t *testing.T) {
	_, h := newTestApp(t)
	w := do(h, "GET", "/", nil, func(r *http.Request) { r.Header.Del("Authorization") })
	if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") == "" {
		t.Errorf("ohne Anmeldung: %d", w.Code)
	}
	w = do(h, "GET", "/", nil, func(r *http.Request) { r.SetBasicAuth("flo", "falsch") })
	if w.Code != http.StatusUnauthorized {
		t.Errorf("falsches Passwort: %d", w.Code)
	}
	w = do(h, "GET", "/healthz", nil, func(r *http.Request) { r.Header.Del("Authorization") })
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), version) {
		t.Errorf("healthz: %d %s", w.Code, w.Body)
	}
	if do(h, "GET", "/", nil, nil).Code != http.StatusOK {
		t.Error("mit Anmeldung")
	}
}

func TestCrossSitePostRejected(t *testing.T) {
	_, h := newTestApp(t)
	form := url.Values{"threshold": {"3"}}
	w := do(h, "POST", "/einstellungen", form, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") })
	if w.Code != http.StatusForbidden {
		t.Errorf("cross-site: %d", w.Code)
	}
	w = do(h, "POST", "/einstellungen", form, func(r *http.Request) {
		r.Header.Del("Sec-Fetch-Site")
		r.Header.Set("Origin", "https://evil.example")
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("fremder Origin: %d", w.Code)
	}
	w = do(h, "POST", "/einstellungen", form, func(r *http.Request) { r.Header.Del("Sec-Fetch-Site") })
	if w.Code != http.StatusForbidden {
		t.Errorf("ohne Herkunft: %d", w.Code)
	}
	w = do(h, "POST", "/einstellungen", form, func(r *http.Request) {
		r.Header.Del("Sec-Fetch-Site")
		r.Header.Set("Origin", "http://"+r.Host)
	})
	if w.Code != http.StatusSeeOther {
		t.Errorf("gleicher Origin: %d", w.Code)
	}
}

func TestCityAndDashboardFlow(t *testing.T) {
	a, h := newTestApp(t)
	w := do(h, "POST", "/staedte", url.Values{"name": {"Ansbach"}, "postcodes": {"91522"}, "rent_per_m2": {"9,50"}, "active": {"1"}}, nil)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("Stadt anlegen: %d %s", w.Code, w.Body)
	}
	w = do(h, "POST", "/staedte", url.Values{"name": {"ansbach"}}, nil)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "schon auf der Liste") {
		t.Errorf("Dublette: %d", w.Code)
	}
	w = do(h, "POST", "/staedte", url.Values{"name": {"X"}, "postcodes": {"9152"}}, nil)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("ungültige PLZ: %d", w.Code)
	}

	// 52,3 m² × 9,50 € = 496,85 €/Monat → 5.962,20 €/Jahr → 4,0 % bei 149.000 €; das Apartment hat eine Ist-Miete
	if _, err := importFile(a.store, "testdata/is24-synthetisch.eml"); err != nil {
		t.Fatal(err)
	}
	body := do(h, "GET", "/", nil, nil).Body.String()
	if !strings.Contains(body, "Kapitalanlage: vermietetes Apartment") {
		t.Error("Inserat über Schwellwert fehlt")
	}
	if strings.Contains(body, "Gepflegte 2-Zimmer-Wohnung") {
		t.Error("Inserat unter Schwellwert sollte fehlen")
	}
	if !strings.Contains(body, "5,2 %") { // Ist-Miete 390 € × 12 / 89.500 €
		t.Errorf("Rendite aus Ist-Miete fehlt:\n%s", body)
	}
	body = do(h, "GET", "/?min=4&sort=preis", nil, nil).Body.String()
	if !strings.Contains(body, "Gepflegte 2-Zimmer-Wohnung") || !strings.Contains(body, "4,0 %") {
		t.Errorf("mit 4 %%: %s", body)
	}
}

func TestManualListingAndHide(t *testing.T) {
	a, h := newTestApp(t)
	w := do(h, "POST", "/inserate", url.Values{"url": {"https://www.immobilienscout24.de/expose/987654321"}, "price": {"100000"}, "area": {"50"}, "rent_actual": {"500"}, "title": {"Testwohnung"}}, nil)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("anlegen: %d %s", w.Code, w.Body)
	}
	ls, _ := a.store.Listings()
	if len(ls) != 1 || ls[0].Source != "is24" || ls[0].ExternalID != "987654321" || ls[0].RentActual != 500 {
		t.Fatalf("%+v", ls)
	}
	id := ls[0].ID
	if !strings.Contains(do(h, "GET", "/", nil, nil).Body.String(), "Testwohnung") {
		t.Error("6 % sollte erscheinen")
	}
	w = do(h, "POST", "/inserate/"+itoa(id)+"/ausblenden", url.Values{"back": {"https://evil.example/"}}, nil)
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/") || strings.Contains(w.Header().Get("Location"), "evil") {
		t.Errorf("ausblenden: %d %s", w.Code, w.Header().Get("Location"))
	}
	if strings.Contains(do(h, "GET", "/", nil, nil).Body.String(), "Testwohnung") {
		t.Error("ausgeblendet sollte fehlen")
	}
	w = do(h, "POST", "/inserate", url.Values{"url": {"javascript:alert(1)"}}, nil)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("ungültige URL: %d", w.Code)
	}
	w = do(h, "GET", "/inserate/"+itoa(id), nil, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Ist-Miete") {
		t.Errorf("Bearbeiten: %d", w.Code)
	}
	if do(h, "GET", "/inserate/999", nil, nil).Code != http.StatusNotFound {
		t.Error("404 erwartet")
	}
}

func TestSecurityHeaders(t *testing.T) {
	_, h := newTestApp(t)
	w := do(h, "GET", "/einstellungen", nil, nil)
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("CSP: %q", csp)
	}
}

func TestCheckAccess(t *testing.T) {
	if err := (Config{Addr: "0.0.0.0:8080"}).checkAccess(); err == nil {
		t.Error("öffentlich ohne Passwort darf nicht starten")
	}
	if err := (Config{Addr: "127.0.0.1:8080"}).checkAccess(); err != nil {
		t.Error(err)
	}
	if err := (Config{Addr: ":8080", User: "a", Password: "b"}).checkAccess(); err != nil {
		t.Error(err)
	}
}

func TestFormatting(t *testing.T) {
	if s := groupThousands(1234567); s != "1.234.567" {
		t.Error(s)
	}
	if s := formatNum(5.4321, 1); s != "5,4" {
		t.Error(s)
	}
	if s := formatNum(12345.678, 2); s != "12.345,68" {
		t.Error(s)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestCitySuggestionsFlow(t *testing.T) {
	a, h := newTestApp(t)
	if _, err := importFile(a.store, "testdata/immowelt-synthetisch.eml"); err != nil {
		t.Fatal(err)
	}
	body := do(h, "GET", "/staedte", nil, nil).Body.String()
	if !strings.Contains(body, "Aus Inseraten erkannt") || !strings.Contains(body, `value="Herrieden"`) || !strings.Contains(body, `value="Ansbach"`) {
		t.Fatalf("Vorschläge fehlen:\n%s", body)
	}
	w := do(h, "POST", "/staedte", url.Values{"name": {"Herrieden"}, "postcodes": {"91567"}, "active": {"1"}, "rent_per_m2": {"8,1"}}, nil)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("übernehmen: %d", w.Code)
	}
	body = do(h, "GET", "/staedte", nil, nil).Body.String()
	if strings.Contains(body, `type="hidden" name="name" value="Herrieden"`) {
		t.Error("übernommene Stadt sollte kein Vorschlag mehr sein")
	}
	if !strings.Contains(body, "8,10 €/m²") {
		t.Error("Mietspiegel nicht übernommen")
	}
}

func TestDashboardExposeLinkAndEditURL(t *testing.T) {
	a, h := newTestApp(t)
	a.store.UpsertListing(Listing{Source: "immowelt", ExternalID: "h-1", URL: "https://click.by.immowelt.de/?qs=abc", Title: "Wohnung A", Price: 100000, RentActual: 600})
	ls, _ := a.store.Listings()
	id := itoa(ls[0].ID)
	body := do(h, "GET", "/", nil, nil).Body.String()
	if !strings.Contains(body, `href="/inserate/`+id+`/expose"`) || !strings.Contains(body, "Exposé ↗") || !strings.Contains(body, "(Weiterleitung)") {
		t.Fatalf("Exposé-Link fehlt:\n%s", body)
	}
	if loc := do(h, "GET", "/inserate/"+id+"/expose", nil, nil).Header().Get("Location"); loc != "https://click.by.immowelt.de/?qs=abc" {
		t.Errorf("Weiterleitung: %q", loc)
	}
	w := do(h, "POST", "/inserate/"+id, url.Values{"url": {"https://www.immowelt.de/expose/xyz99"}, "title": {"Wohnung A"}, "price": {"100000"}, "rent_actual": {"600"}}, nil)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("speichern: %d %s", w.Code, w.Body)
	}
	if loc := do(h, "GET", "/inserate/"+id+"/expose", nil, nil).Header().Get("Location"); loc != "https://www.immowelt.de/expose/xyz99" {
		t.Errorf("neuer Link nicht übernommen: %q", loc)
	}
	if strings.Contains(do(h, "GET", "/", nil, nil).Body.String(), "(Weiterleitung)") {
		t.Error("Markierung „Weiterleitung“ sollte weg sein")
	}
	w = do(h, "POST", "/inserate/"+id, url.Values{"url": {"javascript:alert(1)"}, "price": {"1"}}, nil)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("javascript-URL: %d", w.Code)
	}
}

func TestFavorites(t *testing.T) {
	a, h := newTestApp(t)
	a.store.UpsertListing(Listing{Source: "is24", ExternalID: "1", URL: "https://www.immobilienscout24.de/expose/1", Title: "Rendite-Perle", Price: 100000, RentActual: 600}) // 7,2 %
	a.store.UpsertListing(Listing{Source: "is24", ExternalID: "2", URL: "https://www.immobilienscout24.de/expose/2", Title: "Teures Loft", Price: 400000, RentActual: 1000})  // 3 %
	ls, _ := a.store.Listings()
	ids := map[string]string{}
	for _, l := range ls {
		ids[l.Title] = itoa(l.ID)
	}

	// Stern setzen, Rücksprung auf die gleiche Ansicht samt Anker.
	w := do(h, "POST", "/inserate/"+ids["Teures Loft"]+"/favorit", url.Values{"back": {"/?sort=preis&min=4"}}, nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/?min=4&sort=preis#inserat-"+ids["Teures Loft"] {
		t.Fatalf("favorisieren: %d %q", w.Code, w.Header().Get("Location"))
	}

	// Favorit bleibt sichtbar, obwohl unter dem Schwellwert.
	body := do(h, "GET", "/", nil, nil).Body.String()
	if !strings.Contains(body, "Teures Loft") || !strings.Contains(body, `aria-pressed="true"`) {
		t.Error("Favorit unter Schwellwert sollte sichtbar sein")
	}

	// Filter „nur Favoriten“, auch wenn ausgeblendet.
	a.store.SetHidden(atoi(ids["Teures Loft"]), true)
	body = do(h, "GET", "/?fav=1", nil, nil).Body.String()
	if !strings.Contains(body, "Teures Loft") || strings.Contains(body, "Rendite-Perle") || !strings.Contains(body, "<h1>Favoriten</h1>") {
		t.Errorf("Favoritenfilter:\n%s", body)
	}
	if !strings.Contains(body, "nur Favoriten (1)") {
		t.Error("Anzahl der Favoriten fehlt")
	}

	// Entfernen vom Bearbeiten-Formular aus.
	w = do(h, "POST", "/inserate/"+ids["Teures Loft"]+"/kein-favorit", url.Values{"back": {"edit"}}, nil)
	if !strings.HasPrefix(w.Header().Get("Location"), "/inserate/"+ids["Teures Loft"]) {
		t.Errorf("Rücksprung: %q", w.Header().Get("Location"))
	}
	if strings.Contains(do(h, "GET", "/?fav=1", nil, nil).Body.String(), "Teures Loft") {
		t.Error("sollte kein Favorit mehr sein")
	}
	// Fremde Rücksprungziele werden verworfen.
	w = do(h, "POST", "/inserate/"+ids["Rendite-Perle"]+"/favorit", url.Values{"back": {"//evil.example/"}}, nil)
	if loc := w.Header().Get("Location"); !strings.HasPrefix(loc, "/#") {
		t.Errorf("Rücksprung: %q", loc)
	}
}

func atoi(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }

func TestReadUnread(t *testing.T) {
	a, h := newTestApp(t)
	for i, title := range []string{"Wohnung A", "Wohnung B", "Wohnung C"} {
		a.store.UpsertListing(Listing{Source: "is24", ExternalID: itoa(int64(i + 1)), URL: "https://www.immobilienscout24.de/expose/" + itoa(int64(i+1)), Title: title, Price: 100000, RentActual: 600})
	}
	ids := map[string]string{}
	ls, _ := a.store.Listings()
	for _, l := range ls {
		ids[l.Title] = itoa(l.ID)
		if l.Read {
			t.Fatalf("neu eingelesen sollte ungelesen sein: %+v", l)
		}
	}
	body := do(h, "GET", "/", nil, nil).Body.String()
	if strings.Count(body, ">neu</span>") != 3 || !strings.Contains(body, "Alle 3 als gelesen markieren") {
		t.Fatalf("3 neue erwartet:\n%s", body)
	}

	// Exposé öffnen markiert als gelesen.
	if w := do(h, "GET", "/inserate/"+ids["Wohnung A"]+"/expose", nil, nil); w.Code != http.StatusSeeOther {
		t.Fatalf("expose: %d", w.Code)
	}
	// Detailseite öffnen ebenso.
	do(h, "GET", "/inserate/"+ids["Wohnung B"], nil, nil)
	body = do(h, "GET", "/?neu=1", nil, nil).Body.String()
	if strings.Contains(body, "Wohnung A") || strings.Contains(body, "Wohnung B") || !strings.Contains(body, "Wohnung C") {
		t.Errorf("nur neue:\n%s", body)
	}

	// Wieder als neu markieren.
	do(h, "POST", "/inserate/"+ids["Wohnung A"]+"/ungelesen", url.Values{}, nil)
	if !strings.Contains(do(h, "GET", "/?neu=1", nil, nil).Body.String(), "Wohnung A") {
		t.Error("A sollte wieder neu sein")
	}

	// Alle in der Ansicht als gelesen; Rücksprung ohne „nur neue“.
	w := do(h, "POST", "/gelesen", url.Values{"back": {"/?neu=1&sort=preis"}}, nil)
	if loc := w.Header().Get("Location"); !strings.HasPrefix(loc, "/?sort=preis&ok=") {
		t.Errorf("Rücksprung: %q", loc)
	}
	if body := do(h, "GET", "/?neu=1", nil, nil).Body.String(); !strings.Contains(body, "alles gelesen") {
		t.Errorf("sollte leer sein:\n%s", body)
	}

	// Preissenkung macht ein Inserat wieder neu, Preiserhöhung nicht.
	a.store.UpsertListing(Listing{Source: "is24", ExternalID: "2", Price: 120000})
	a.store.UpsertListing(Listing{Source: "is24", ExternalID: "3", Price: 95000})
	l2, _ := a.store.Listing(atoi(ids["Wohnung B"]))
	l3, _ := a.store.Listing(atoi(ids["Wohnung C"]))
	if !l2.Read || l3.Read {
		t.Errorf("nach Preisänderung: B gelesen=%v, C gelesen=%v", l2.Read, l3.Read)
	}
	if do(h, "GET", "/inserate/999/expose", nil, nil).Code != http.StatusNotFound {
		t.Error("404 erwartet")
	}
}
