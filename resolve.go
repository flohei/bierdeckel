package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Tracking-Links aus den Alert-Mails (immowelt: click.by.immowelt.de) werden beim Einlesen
// einmal aufgelöst, damit das Dashboard direkt aufs Exposé verlinkt. Es werden nur die
// Weiterleitungen verfolgt – die Exposé-Seite selbst wird nie abgerufen.

// Nur Links dieser Domains werden aufgelöst (keine beliebigen URLs aus Mails abrufen).
var resolvableHosts = []string{"immowelt.de", "immobilienscout24.de", "kleinanzeigen.de"}

var errNoExpose = errors.New("Weiterleitung führt zu keinem Exposé")

// resolveTransport ist nur für Tests gesetzt (lokaler TLS-Server).
var resolveTransport http.RoundTripper

func resolvable(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range resolvableHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// resolveExpose folgt den Weiterleitungen eines Tracking-Links bis zur Exposé-URL.
// In Tests ersetzbar.
var resolveExpose = realResolveExpose

func realResolveExpose(ctx context.Context, raw string) (source, id, canonical string, err error) {
	if !resolvable(raw) {
		return "", "", "", errNoExpose
	}
	var found bool
	client := &http.Client{
		Transport: resolveTransport,
		Timeout:   15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if s, i, c, ok := exposeFromURL(req.URL.String()); ok {
				source, id, canonical, found = s, i, c, true
				return http.ErrUseLastResponse
			}
			if len(via) >= 6 || req.URL.Scheme != "https" {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Bierdeckel/"+version+"; private alert reader)")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", "", err
	}
	resp.Body.Close()
	if !found {
		return "", "", "", errNoExpose
	}
	return source, id, canonical, nil
}

// resolveLinks ersetzt Tracking-Links durch Exposé-URLs; die Exposé-ID ersetzt dann auch
// die Hash-ID. Schlägt das Auflösen fehl, bleibt der Tracking-Link (er funktioniert im Browser).
func resolveLinks(ctx context.Context, ls []ParsedListing) []ParsedListing {
	cache := map[string]bool{}
	for i := range ls {
		l := &ls[i]
		if _, _, _, ok := exposeFromURL(l.URL); ok || l.URL == "" || cache[l.URL] {
			continue
		}
		src, id, canon, err := resolveExpose(ctx, l.URL)
		if err != nil {
			cache[l.URL] = true
			continue
		}
		l.Source, l.ExternalID, l.URL, l.Hashed = src, id, canon, false
	}
	return ls
}
