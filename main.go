// Bierdeckel – privates Dashboard für Kapitalanlage-Wohnungen nach Bierdeckelrechnung.
//
// Inserate kommen aus den Suchauftrags-Mails von ImmoScout24 und immowelt (per IMAP),
// die Miete aus einem pro Stadt hinterlegten Mietspiegel-Wert (€/m²).
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// version wird beim Release-Build per -ldflags "-X main.version=…" gesetzt.
var version = "dev"

// Config wird ausschließlich über Umgebungsvariablen gesetzt.
type Config struct {
	Addr         string        // BIERDECKEL_ADDR
	DataDir      string        // BIERDECKEL_DATA
	User         string        // BIERDECKEL_USER (Basic Auth)
	Password     string        // BIERDECKEL_PASSWORD (Basic Auth)
	IMAPAddr     string        // BIERDECKEL_IMAP_ADDR, z. B. imap.example.org:993
	IMAPUser     string        // BIERDECKEL_IMAP_USER
	IMAPPassword string        // BIERDECKEL_IMAP_PASSWORD
	IMAPMailbox  string        // BIERDECKEL_IMAP_MAILBOX (Standard: INBOX)
	Interval     time.Duration // BIERDECKEL_INTERVAL (Standard: 30m)
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func loadConfig() (Config, error) {
	c := Config{
		Addr:         env("BIERDECKEL_ADDR", "127.0.0.1:8080"),
		DataDir:      env("BIERDECKEL_DATA", "./data"),
		User:         env("BIERDECKEL_USER", ""),
		Password:     os.Getenv("BIERDECKEL_PASSWORD"),
		IMAPAddr:     env("BIERDECKEL_IMAP_ADDR", ""),
		IMAPUser:     env("BIERDECKEL_IMAP_USER", ""),
		IMAPPassword: os.Getenv("BIERDECKEL_IMAP_PASSWORD"),
		IMAPMailbox:  env("BIERDECKEL_IMAP_MAILBOX", "INBOX"),
	}
	iv, err := time.ParseDuration(env("BIERDECKEL_INTERVAL", "30m"))
	if err != nil || iv < time.Minute {
		return c, fmt.Errorf("BIERDECKEL_INTERVAL ungültig (mindestens 1m): %q", os.Getenv("BIERDECKEL_INTERVAL"))
	}
	c.Interval = iv
	return c, nil
}

// checkAccess verhindert, dass das Dashboard versehentlich ohne Passwort öffentlich erreichbar ist.
func (c Config) checkAccess() error {
	if c.User != "" && c.Password != "" {
		return nil
	}
	host, _, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return fmt.Errorf("BIERDECKEL_ADDR: %w", err)
	}
	if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
		return nil
	}
	return errors.New("ohne BIERDECKEL_USER und BIERDECKEL_PASSWORD lauscht Bierdeckel nur auf localhost")
}

func (c Config) imapEnabled() bool { return c.IMAPAddr != "" && c.IMAPUser != "" }

const usage = `Bierdeckel

Verwendung:
  bierdeckel [serve]          Webserver starten, ruft regelmäßig das Postfach ab (Standard)
  bierdeckel fetch            Postfach einmal abrufen und Inserate speichern
  bierdeckel import <datei.eml>…
                              Gespeicherte Alert-Mails einlesen (zum Testen und Nachtragen)
  bierdeckel parse <datei.eml>
                              Mail nur analysieren und erkannte Inserate ausgeben (speichert nichts)
  bierdeckel reimport         Aus Mails gelesene Inserate löschen und beim nächsten Abruf
                              neu einlesen (nach Parser-Verbesserungen; Notizen bleiben)
  bierdeckel version          Version ausgeben

Konfiguration über Umgebungsvariablen, siehe README.md.
`

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	case "version", "--version":
		fmt.Println(version)
		return
	case "parse":
		if len(os.Args) != 3 {
			log.Fatal("Verwendung: bierdeckel parse <datei.eml>")
		}
		if err := parseCmd(os.Args[2]); err != nil {
			log.Fatal(err)
		}
		return
	}

	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		log.Fatalf("Datenverzeichnis: %v", err)
	}
	db, err := openDB(filepath.Join(cfg.DataDir, "bierdeckel.db"))
	if err != nil {
		log.Fatalf("Datenbank: %v", err)
	}
	defer db.Close()
	store := &Store{db: db}

	switch cmd {
	case "serve":
		if err := serve(cfg, store); err != nil {
			log.Fatal(err)
		}
	case "fetch":
		if !cfg.imapEnabled() {
			log.Fatal("BIERDECKEL_IMAP_ADDR und BIERDECKEL_IMAP_USER sind nicht gesetzt")
		}
		res := fetchMail(context.Background(), cfg, store)
		fmt.Println(res.Summary())
		if res.Err != nil {
			os.Exit(1)
		}
	case "reimport":
		n, err := store.ResetImported()
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%d Inserate gelöscht. Der nächste Abruf liest alle Mails neu ein.\n", n)
	case "import":
		if len(os.Args) < 3 {
			log.Fatal("Verwendung: bierdeckel import <datei.eml>…")
		}
		total := 0
		for _, path := range os.Args[2:] {
			n, err := importFile(store, path)
			if err != nil {
				log.Fatalf("%s: %v", path, err)
			}
			fmt.Printf("%s: %d Inserate\n", path, n)
			total += n
		}
		fmt.Printf("Gesamt: %d Inserate\n", total)
	default:
		fmt.Print(usage)
		os.Exit(2)
	}
}

func serve(cfg Config, store *Store) error {
	if err := cfg.checkAccess(); err != nil {
		return err
	}
	app, err := newApp(cfg, store)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           app.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.imapEnabled() {
		go app.fetchLoop(ctx)
	} else {
		log.Print("Kein IMAP-Postfach konfiguriert – nur manuelle Inserate und „import“")
	}

	errc := make(chan error, 1)
	go func() {
		log.Printf("Bierdeckel %s lauscht auf %s", version, cfg.Addr)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

// atoiDefault liest eine Ganzzahl aus einem Formularfeld.
func atoiDefault(s string, def int64) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return def
	}
	return n
}
