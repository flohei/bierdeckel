package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Kein Test darf echte Anfragen ins Netz schicken.
func TestMain(m *testing.M) {
	resolveExpose = func(context.Context, string) (string, string, string, error) {
		return "", "", "", errors.New("im Test kein Netz")
	}
	os.Exit(m.Run())
}

func stubResolver(t *testing.T, targets map[string]string) {
	old := resolveExpose
	resolveExpose = func(_ context.Context, raw string) (string, string, string, error) {
		if dst, ok := targets[raw]; ok {
			if s, id, c, ok := exposeFromURL(dst); ok {
				return s, id, c, nil
			}
		}
		return "", "", "", errNoExpose
	}
	t.Cleanup(func() { resolveExpose = old })
}

func TestRealResolverFollowsRedirectsUntilExpose(t *testing.T) {
	var exposeHit bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/r":
			http.Redirect(w, r, "/r2?x=1", http.StatusFound)
		case "/r2":
			// Zielseite liegt bei immowelt – darf nicht mehr abgerufen werden.
			http.Redirect(w, r, "https://www.immowelt.de/expose/2b7kq5x?utm_source=alert", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		default:
			exposeHit = true
		}
	}))
	defer srv.Close()

	// Echte Implementierung gegen den Testserver; Host-Allowlist und Transport vorübergehend anpassen.
	real := resolveReal(t)
	oldHosts, oldTransport := resolvableHosts, resolveTransport
	resolvableHosts = append([]string{"127.0.0.1"}, oldHosts...)
	resolveTransport = srv.Client().Transport
	t.Cleanup(func() { resolvableHosts, resolveTransport = oldHosts, oldTransport })

	src, id, canon, err := real(context.Background(), srv.URL+"/r")
	if err != nil || src != "immowelt" || id != "2b7kq5x" || canon != "https://www.immowelt.de/expose/2b7kq5x" {
		t.Fatalf("%s %s %s %v", src, id, canon, err)
	}
	if exposeHit {
		t.Error("Exposé-Seite wurde abgerufen")
	}
	if _, _, _, err := real(context.Background(), srv.URL+"/loop"); err == nil {
		t.Error("Endlos-Weiterleitung sollte fehlschlagen")
	}
	if _, _, _, err := real(context.Background(), "https://evil.example/r"); err != errNoExpose {
		t.Errorf("fremde Domain darf nicht abgerufen werden: %v", err)
	}
	if _, _, _, err := real(context.Background(), "http://click.by.immowelt.de/r"); err != errNoExpose {
		t.Errorf("nur https: %v", err)
	}
}

func TestImportResolvesTrackingLinks(t *testing.T) {
	stubResolver(t, map[string]string{
		"https://click.by.immowelt.de/?qs=FAKE0011TOKENxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx": "https://www.immowelt.de/expose/abc123x?bd=1",
	})
	s := newTestStore(t)
	raw, _ := os.ReadFile(filepath.Join("testdata", "immowelt-2026-10.eml"))
	if _, _, err := importMessage(context.Background(), s, raw); err != nil {
		t.Fatal(err)
	}
	ls, _ := s.Listings()
	if len(ls) != 2 {
		t.Fatalf("%d Inserate", len(ls))
	}
	var resolved, tracking int
	for _, l := range ls {
		switch {
		case l.URL == "https://www.immowelt.de/expose/abc123x" && l.ExternalID == "abc123x":
			resolved++
		case strings.HasPrefix(l.URL, "https://click.by.immowelt.de/") && strings.HasPrefix(l.ExternalID, "h-"):
			tracking++ // nicht auflösbar: Weiterleitung bleibt als Link
		}
	}
	if resolved != 1 || tracking != 1 {
		t.Errorf("aufgelöst %d, Weiterleitung %d: %+v", resolved, tracking, ls)
	}
}

func TestUpsertUpgradesTrackingLink(t *testing.T) {
	s := newTestStore(t)
	s.UpsertListing(Listing{Source: "is24", ExternalID: "1", URL: "https://click.example/x"})
	s.UpsertListing(Listing{Source: "is24", ExternalID: "1", URL: "https://www.immobilienscout24.de/expose/1"})
	s.UpsertListing(Listing{Source: "is24", ExternalID: "1", URL: "https://click.example/y"})
	ls, _ := s.Listings()
	if ls[0].URL != "https://www.immobilienscout24.de/expose/1" {
		t.Errorf("URL: %s", ls[0].URL)
	}
}

// resolveReal liefert die echte Implementierung, die TestMain überschrieben hat.
func resolveReal(t *testing.T) func(context.Context, string) (string, string, string, error) {
	t.Helper()
	return realResolveExpose
}
