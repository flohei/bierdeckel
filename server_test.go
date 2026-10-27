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
