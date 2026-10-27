package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

type App struct {
	cfg    Config
	store  *Store
	tmpl   map[string]*template.Template
	assets map[string]string // Dateiname -> Hash für Cache-Busting
	now    func() time.Time

	fetchMu  sync.Mutex
	fetching bool
	trigger  chan struct{}
}

func newApp(cfg Config, store *Store) (*App, error) {
	a := &App{cfg: cfg, store: store, assets: map[string]string{}, now: time.Now, trigger: make(chan struct{}, 1)}
	if err := a.loadAssets(); err != nil {
		return nil, err
	}
	if err := a.loadTemplates(); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *App) loadAssets() error {
	return fs.WalkDir(staticFS, "static", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := staticFS.ReadFile(path)
		if err != nil {
			return err
		}
		h := sha256.Sum256(b)
		a.assets[strings.TrimPrefix(path, "static/")] = hex.EncodeToString(h[:6])
		return nil
	})
}

// ---- Formatierung (deutsch) ----

func groupThousands(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func formatNum(f float64, decimals int) string {
	if decimals == 0 {
		return groupThousands(int64(math.Round(f)))
	}
	s := strconv.FormatFloat(f, 'f', decimals, 64)
	intPart, frac, _ := strings.Cut(s, ".")
	n, _ := strconv.ParseInt(intPart, 10, 64)
	return groupThousands(n) + "," + frac
}

// formatInput gibt Zahlen für Formularfelder aus (ohne Tausenderpunkte).
func formatInput(f float64) string {
	if f == 0 {
		return ""
	}
	return strings.ReplaceAll(strconv.FormatFloat(f, 'f', -1, 64), ".", ",")
}

func (a *App) loadTemplates() error {
	funcs := template.FuncMap{
		"asset":   func(name string) string { return "/static/" + name + "?v=" + a.assets[name] },
		"version": func() string { return version },
		"eur": func(n int64) string {
			if n == 0 {
				return "–"
			}
			return groupThousands(n) + " €"
		},
		"num": formatNum,
		"in":  formatInput,
		"inInt": func(n int64) string {
			if n == 0 {
				return ""
			}
			return strconv.FormatInt(n, 10)
		},
		"datetime": func(t time.Time) string { return t.In(berlin).Format("02.01.2006, 15:04") },
		"date":     func(t time.Time) string { return t.In(berlin).Format("02.01.2006") },
		"since": func(t time.Time) string {
			d := a.now().Sub(t)
			switch {
			case d < time.Hour:
				return "gerade eben"
			case d < 24*time.Hour:
				return fmt.Sprintf("vor %d Std.", int(d.Hours()))
			case d < 48*time.Hour:
				return "gestern"
			default:
				return fmt.Sprintf("vor %d Tagen", int(d.Hours()/24))
			}
		},
		"sourceLabel": func(s string) string {
			switch s {
			case "is24":
				return "ImmoScout24"
			case "immowelt":
				return "immowelt"
			case "kleinanzeigen":
				return "Kleinanzeigen"
			}
			return "manuell"
		},
		"join":     strings.Join,
		"sortKeys": func() []string { return []string{"rendite", "neu", "preis", "qm"} },
		"sortLabel": func(k string) string {
			return sortKeys[k]
		},
		"yieldClass": func(r Row, threshold float64) string {
			switch {
			case r.Yield == 0:
				return "yield-none"
			case r.Yield >= threshold:
				return "yield-good"
			case r.Yield >= threshold-1:
				return "yield-near"
			}
			return "yield-low"
		},
		"isIndex":  func(s RentSource) bool { return s == RentIndex },
		"isActual": func(s RentSource) bool { return s == RentActual },
	}
	pages, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return err
	}
	a.tmpl = map[string]*template.Template{}
	for _, p := range pages {
		name := strings.TrimPrefix(p, "templates/")
		if name == "layout.html" {
			continue
		}
		t, err := template.New(name).Funcs(funcs).ParseFS(templateFS, "templates/layout.html", p)
		if err != nil {
			return fmt.Errorf("Template %s: %w", name, err)
		}
		a.tmpl[name] = t
	}
	return nil
}

var berlin = func() *time.Location {
	if l, err := time.LoadLocation("Europe/Berlin"); err == nil {
		return l
	}
	return time.Local
}()

func (a *App) render(w http.ResponseWriter, r *http.Request, status int, name string, data map[string]any) {
	t, ok := a.tmpl[name]
	if !ok {
		http.Error(w, "Template fehlt: "+name, http.StatusInternalServerError)
		return
	}
	if data == nil {
		data = map[string]any{}
	}
	if f := r.URL.Query().Get("ok"); f != "" {
		data["Flash"] = f
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		log.Printf("Template %s: %v", name, err)
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

func (a *App) fail(w http.ResponseWriter, r *http.Request, status int, msg string) {
	a.render(w, r, status, "error.html", map[string]any{"Title": "Fehler", "Message": msg})
}

func (a *App) serverError(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
	a.fail(w, r, http.StatusInternalServerError, "Da ist etwas schiefgegangen.")
}

func redirect(w http.ResponseWriter, r *http.Request, path, flash string) {
	if flash != "" {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		path += sep + "ok=" + url.QueryEscape(flash)
	}
	http.Redirect(w, r, path, http.StatusSeeOther)
}

// ---- Routen und Middleware ----

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok " + version)) })
	static, _ := fs.Sub(staticFS, "static")
	fileServer := http.FileServer(http.FS(static))
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		fileServer.ServeHTTP(w, r)
	})))

	app := http.NewServeMux()
	app.HandleFunc("GET /{$}", a.dashboard)
	app.HandleFunc("GET /inserate/neu", a.listingNewForm)
	app.HandleFunc("POST /inserate", a.listingCreate)
	app.HandleFunc("GET /inserate/{id}", a.listingEditForm)
	app.HandleFunc("POST /inserate/{id}", a.listingUpdate)
	app.HandleFunc("POST /inserate/{id}/ausblenden", a.listingHide(true))
	app.HandleFunc("POST /inserate/{id}/einblenden", a.listingHide(false))
	app.HandleFunc("POST /inserate/{id}/loeschen", a.listingDelete)
	app.HandleFunc("GET /staedte", a.citiesPage)
	app.HandleFunc("POST /staedte", a.cityCreate)
	app.HandleFunc("GET /staedte/{id}", a.cityEditForm)
	app.HandleFunc("POST /staedte/{id}", a.cityUpdate)
	app.HandleFunc("POST /staedte/{id}/loeschen", a.cityDelete)
	app.HandleFunc("GET /einstellungen", a.settingsPage)
	app.HandleFunc("POST /einstellungen", a.settingsSave)
	app.HandleFunc("POST /abruf", a.fetchTrigger)
	mux.Handle("/", a.requireAuth(a.sameOrigin(app)))

	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Robots-Tag", "noindex, nofollow")
		next.ServeHTTP(w, r)
	})
}

// requireAuth schützt alles außer /healthz und /static per HTTP Basic Auth.
// Ohne konfigurierte Zugangsdaten lauscht der Server nur auf localhost (siehe Config.checkAccess).
func (a *App) requireAuth(next http.Handler) http.Handler {
	if a.cfg.User == "" || a.cfg.Password == "" {
		return next
	}
	wantUser := sha256.Sum256([]byte(a.cfg.User))
	wantPass := sha256.Sum256([]byte(a.cfg.Password))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		gotUser, gotPass := sha256.Sum256([]byte(u)), sha256.Sum256([]byte(p))
		if !ok || subtle.ConstantTimeCompare(gotUser[:], wantUser[:])&subtle.ConstantTimeCompare(gotPass[:], wantPass[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="Bierdeckel", charset="UTF-8"`)
			http.Error(w, "Anmeldung erforderlich", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin wehrt CSRF ab: Browser senden Basic-Auth-Daten auch bei fremdinitiierten Formularen mit.
func (a *App) sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		ok := false
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
			ok = site == "same-origin" || site == "none"
		} else if origin := r.Header.Get("Origin"); origin != "" {
			if u, err := url.Parse(origin); err == nil {
				ok = u.Host == r.Host
			}
		}
		if !ok {
			http.Error(w, "Anfrage von fremder Seite abgelehnt", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func pathID(r *http.Request) int64 { return atoiDefault(r.PathValue("id"), 0) }

// ---- Dashboard ----

func (a *App) dashboard(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	saved := a.store.Threshold()
	f := Filter{
		Threshold:  saved,
		All:        q.Get("alle") == "1",
		ShowHidden: q.Get("ausgeblendet") == "1",
		OnlyActive: q.Get("inaktive") != "1",
		CityID:     atoiDefault(q.Get("stadt"), 0),
		Sort:       q.Get("sort"),
	}
	if v := parseGermanFloat(q.Get("min")); v > 0 {
		f.Threshold = v
	}
	if _, ok := sortKeys[f.Sort]; !ok {
		f.Sort = "rendite"
	}
	listings, err := a.store.Listings()
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	cities, err := a.store.Cities()
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	rows := buildRows(listings, cities, f, a.now())

	var missingRent []string
	for _, c := range cities {
		if c.Active && c.RentPerM2 == 0 {
			missingRent = append(missingRent, c.Name)
		}
	}
	unmatched := 0
	for _, l := range listings {
		if !l.Hidden && matchCity(l, cities) == nil && l.RentActual == 0 {
			unmatched++
		}
	}
	last, _ := a.store.RecentFetches(1)
	data := map[string]any{
		"Title":          "Inserate",
		"Nav":            "dashboard",
		"Rows":           rows,
		"Filter":         f,
		"MinInput":       formatInput(f.Threshold),
		"Cities":         cities,
		"MissingRent":    missingRent,
		"Unmatched":      unmatched,
		"TotalListings":  len(listings),
		"IMAP":           a.cfg.imapEnabled(),
		"Fetching":       a.isFetching(),
		"SavedThreshold": saved,
	}
	if len(last) > 0 {
		data["LastFetch"] = last[0]
	}
	a.render(w, r, http.StatusOK, "dashboard.html", data)
}

// ---- Inserate ----

func listingFromForm(r *http.Request) Listing {
	return Listing{
		Title:      strings.TrimSpace(r.PostFormValue("title")),
		Address:    strings.TrimSpace(r.PostFormValue("address")),
		Postcode:   strings.TrimSpace(r.PostFormValue("postcode")),
		Price:      int64(parseGermanFloat(r.PostFormValue("price"))),
		Area:       parseGermanFloat(r.PostFormValue("area")),
		Rooms:      parseGermanFloat(r.PostFormValue("rooms")),
		RentActual: int64(parseGermanFloat(r.PostFormValue("rent_actual"))),
		Note:       strings.TrimSpace(r.PostFormValue("note")),
	}
}

func validateListing(l Listing) string {
	switch {
	case l.Price < 0 || l.Area < 0 || l.Rooms < 0 || l.RentActual < 0:
		return "Negative Werte sind nicht erlaubt."
	case l.Postcode != "" && !plzRe.MatchString(l.Postcode):
		return "Die Postleitzahl muss fünfstellig sein."
	}
	return ""
}

func (a *App) listingNewForm(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, http.StatusOK, "listing_form.html", map[string]any{"Title": "Inserat hinzufügen", "Nav": "dashboard", "New": true, "L": Listing{}})
}

func (a *App) listingCreate(w http.ResponseWriter, r *http.Request) {
	l := listingFromForm(r)
	raw := strings.TrimSpace(r.PostFormValue("url"))
	u, err := url.Parse(raw)
	if raw == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		a.render(w, r, http.StatusUnprocessableEntity, "listing_form.html", map[string]any{"Title": "Inserat hinzufügen", "Nav": "dashboard", "New": true, "L": l, "URL": raw, "Error": "Bitte einen gültigen Link zum Inserat angeben."})
		return
	}
	if msg := validateListing(l); msg != "" {
		a.render(w, r, http.StatusUnprocessableEntity, "listing_form.html", map[string]any{"Title": "Inserat hinzufügen", "Nav": "dashboard", "New": true, "L": l, "URL": raw, "Error": msg})
		return
	}
	if src, id, canon, ok := exposeFromURL(raw); ok {
		l.Source, l.ExternalID, l.URL = src, id, canon
	} else {
		h := sha256.Sum256([]byte(raw))
		l.Source, l.ExternalID, l.URL = "manual", hex.EncodeToString(h[:8]), raw
	}
	if _, err := a.store.UpsertListing(l); err != nil {
		a.serverError(w, r, err)
		return
	}
	// Notiz und Korrekturen auch bei bereits bekannten Inseraten übernehmen.
	var id int64
	if err := a.store.db.QueryRow(`SELECT id FROM listings WHERE source = ? AND external_id = ?`, l.Source, l.ExternalID).Scan(&id); err == nil {
		existing, _ := a.store.Listing(id)
		merged := mergeManual(existing, l)
		if err := a.store.UpdateListingManual(merged); err != nil {
			a.serverError(w, r, err)
			return
		}
	}
	redirect(w, r, "/?alle=1&sort=neu", "Inserat gespeichert.")
}

// mergeManual übernimmt nur ausgefüllte Formularfelder.
func mergeManual(old, in Listing) Listing {
	if in.Title != "" {
		old.Title = in.Title
	}
	if in.Address != "" {
		old.Address = in.Address
	}
	if in.Postcode != "" {
		old.Postcode = in.Postcode
	}
	if in.Price > 0 {
		old.Price = in.Price
	}
	if in.Area > 0 {
		old.Area = in.Area
	}
	if in.Rooms > 0 {
		old.Rooms = in.Rooms
	}
	if in.RentActual > 0 {
		old.RentActual = in.RentActual
	}
	if in.Note != "" {
		old.Note = in.Note
	}
	return old
}

func (a *App) listingEditForm(w http.ResponseWriter, r *http.Request) {
	l, err := a.store.Listing(pathID(r))
	if errors.Is(err, sql.ErrNoRows) {
		a.fail(w, r, http.StatusNotFound, "Inserat nicht gefunden.")
		return
	} else if err != nil {
		a.serverError(w, r, err)
		return
	}
	cities, _ := a.store.Cities()
	row := evaluate(l, cities, a.now())
	a.render(w, r, http.StatusOK, "listing_form.html", map[string]any{"Title": "Inserat bearbeiten", "Nav": "dashboard", "L": l, "Row": row})
}

func (a *App) listingUpdate(w http.ResponseWriter, r *http.Request) {
	old, err := a.store.Listing(pathID(r))
	if errors.Is(err, sql.ErrNoRows) {
		a.fail(w, r, http.StatusNotFound, "Inserat nicht gefunden.")
		return
	} else if err != nil {
		a.serverError(w, r, err)
		return
	}
	l := listingFromForm(r)
	l.ID, l.Source, l.ExternalID, l.URL = old.ID, old.Source, old.ExternalID, old.URL
	if msg := validateListing(l); msg != "" {
		a.render(w, r, http.StatusUnprocessableEntity, "listing_form.html", map[string]any{"Title": "Inserat bearbeiten", "Nav": "dashboard", "L": l, "Error": msg})
		return
	}
	if err := a.store.UpdateListingManual(l); err != nil {
		a.serverError(w, r, err)
		return
	}
	redirect(w, r, "/inserate/"+strconv.FormatInt(l.ID, 10), "Gespeichert.")
}

func (a *App) listingHide(hide bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := a.store.SetHidden(pathID(r), hide); err != nil {
			a.serverError(w, r, err)
			return
		}
		back := safeBack(r.PostFormValue("back"))
		msg := "Inserat ausgeblendet."
		if !hide {
			msg = "Inserat wieder eingeblendet."
		}
		redirect(w, r, back, msg)
	}
}

func (a *App) listingDelete(w http.ResponseWriter, r *http.Request) {
	if err := a.store.DeleteListing(pathID(r)); err != nil {
		a.serverError(w, r, err)
		return
	}
	redirect(w, r, "/", "Inserat gelöscht.")
}

// safeBack lässt nur relative Rücksprünge auf das Dashboard zu.
func safeBack(s string) string {
	if strings.HasPrefix(s, "/?") || s == "/" {
		if u, err := url.Parse(s); err == nil {
			q := u.Query()
			q.Del("ok")
			if enc := q.Encode(); enc != "" {
				return "/?" + enc
			}
		}
	}
	return "/"
}

// ---- Städte ----

func cityFromForm(r *http.Request) (City, string) {
	c := City{
		Name:      strings.TrimSpace(r.PostFormValue("name")),
		Postcodes: splitList(r.PostFormValue("postcodes")),
		RentPerM2: parseGermanFloat(r.PostFormValue("rent_per_m2")),
		Active:    r.PostFormValue("active") == "1",
		Note:      strings.TrimSpace(r.PostFormValue("note")),
	}
	switch {
	case c.Name == "":
		return c, "Bitte einen Namen angeben."
	case c.RentPerM2 < 0 || c.RentPerM2 > 100:
		return c, "Der Mietspiegel-Wert muss zwischen 0 und 100 €/m² liegen."
	}
	for _, p := range c.Postcodes {
		if len(p) != 5 || !plzRe.MatchString(p) {
			return c, "Postleitzahlen müssen fünfstellig sein: " + p
		}
	}
	return c, ""
}

func (a *App) citiesPage(w http.ResponseWriter, r *http.Request) {
	a.renderCities(w, r, http.StatusOK, City{Active: true}, "")
}

func (a *App) renderCities(w http.ResponseWriter, r *http.Request, status int, draft City, errMsg string) {
	cities, err := a.store.Cities()
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	listings, err := a.store.Listings()
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	counts := map[int64]int{}
	for _, l := range listings {
		if c := matchCity(l, cities); c != nil {
			counts[c.ID]++
		}
	}
	a.render(w, r, status, "cities.html", map[string]any{"Title": "Städte", "Nav": "cities", "Cities": cities, "Counts": counts, "Draft": draft, "Error": errMsg})
}

func (a *App) cityCreate(w http.ResponseWriter, r *http.Request) {
	c, msg := cityFromForm(r)
	if msg == "" {
		if err := a.store.SaveCity(&c); errors.Is(err, errDuplicateCity) {
			msg = "„" + c.Name + "“ steht schon auf der Liste."
		} else if err != nil {
			a.serverError(w, r, err)
			return
		}
	}
	if msg != "" {
		a.renderCities(w, r, http.StatusUnprocessableEntity, c, msg)
		return
	}
	redirect(w, r, "/staedte", c.Name+" hinzugefügt.")
}

func (a *App) cityEditForm(w http.ResponseWriter, r *http.Request) {
	c, err := a.store.City(pathID(r))
	if errors.Is(err, sql.ErrNoRows) {
		a.fail(w, r, http.StatusNotFound, "Stadt nicht gefunden.")
		return
	} else if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, http.StatusOK, "city_form.html", map[string]any{"Title": c.Name, "Nav": "cities", "C": c})
}

func (a *App) cityUpdate(w http.ResponseWriter, r *http.Request) {
	c, msg := cityFromForm(r)
	c.ID = pathID(r)
	if msg == "" {
		if err := a.store.SaveCity(&c); errors.Is(err, errDuplicateCity) {
			msg = "„" + c.Name + "“ steht schon auf der Liste."
		} else if err != nil {
			a.serverError(w, r, err)
			return
		}
	}
	if msg != "" {
		a.render(w, r, http.StatusUnprocessableEntity, "city_form.html", map[string]any{"Title": "Stadt bearbeiten", "Nav": "cities", "C": c, "Error": msg})
		return
	}
	redirect(w, r, "/staedte", c.Name+" gespeichert.")
}

func (a *App) cityDelete(w http.ResponseWriter, r *http.Request) {
	if err := a.store.DeleteCity(pathID(r)); err != nil {
		a.serverError(w, r, err)
		return
	}
	redirect(w, r, "/staedte", "Stadt entfernt.")
}

// ---- Einstellungen und Abruf ----

func (a *App) settingsPage(w http.ResponseWriter, r *http.Request) {
	fetches, err := a.store.RecentFetches(15)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, http.StatusOK, "settings.html", map[string]any{
		"Title":     "Einstellungen",
		"Nav":       "settings",
		"Threshold": formatInput(a.store.Threshold()),
		"Fetches":   fetches,
		"IMAP":      a.cfg.imapEnabled(),
		"Mailbox":   a.cfg.IMAPMailbox,
		"IMAPUser":  a.cfg.IMAPUser,
		"IMAPAddr":  a.cfg.IMAPAddr,
		"Interval":  a.cfg.Interval.String(),
		"Fetching":  a.isFetching(),
	})
}

func (a *App) settingsSave(w http.ResponseWriter, r *http.Request) {
	v := parseGermanFloat(r.PostFormValue("threshold"))
	if v <= 0 || v > 50 {
		a.fail(w, r, http.StatusUnprocessableEntity, "Der Schwellwert muss zwischen 0 und 50 % liegen.")
		return
	}
	if err := a.store.SetThreshold(v); err != nil {
		a.serverError(w, r, err)
		return
	}
	redirect(w, r, "/einstellungen", "Schwellwert gespeichert.")
}

func (a *App) fetchTrigger(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.imapEnabled() {
		a.fail(w, r, http.StatusConflict, "Kein Postfach konfiguriert (BIERDECKEL_IMAP_*).")
		return
	}
	select {
	case a.trigger <- struct{}{}:
	default: // läuft schon oder ist angestoßen
	}
	redirect(w, r, "/einstellungen", "Abruf gestartet – Ergebnis erscheint gleich unten.")
}

func (a *App) isFetching() bool {
	a.fetchMu.Lock()
	defer a.fetchMu.Unlock()
	return a.fetching
}

// fetchLoop ruft das Postfach beim Start, im Intervall und auf Knopfdruck ab.
func (a *App) fetchLoop(ctx context.Context) {
	run := func() {
		a.fetchMu.Lock()
		a.fetching = true
		a.fetchMu.Unlock()
		res := fetchMail(ctx, a.cfg, a.store)
		a.fetchMu.Lock()
		a.fetching = false
		a.fetchMu.Unlock()
		log.Printf("Abruf: %s", res.Summary())
	}
	run()
	t := time.NewTicker(a.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		case <-a.trigger:
			run()
		}
	}
}
