package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Ein bewusst minimaler IMAP-Client (RFC 3501): LOGIN, EXAMINE, UID SEARCH, UID FETCH.
// Das Postfach wird nur gelesen (EXAMINE, BODY.PEEK) – Mails bleiben ungelesen und unverändert.

const maxMessagesPerRun = 300

type imapConn struct {
	c   net.Conn
	r   *bufio.Reader
	tag int
}

// dialIMAP wird in Tests durch eine unverschlüsselte Verbindung ersetzt.
var dialIMAP = func(ctx context.Context, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	d := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 20 * time.Second}, Config: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}}
	return d.DialContext(ctx, "tcp", addr)
}

func connectIMAP(ctx context.Context, addr string) (*imapConn, error) {
	c, err := dialIMAP(ctx, addr)
	if err != nil {
		return nil, err
	}
	c.SetDeadline(time.Now().Add(3 * time.Minute))
	ic := &imapConn{c: c, r: bufio.NewReader(c)}
	greet, _, err := ic.readLine()
	if err != nil {
		c.Close()
		return nil, err
	}
	if !strings.HasPrefix(greet, "* OK") && !strings.HasPrefix(greet, "* PREAUTH") {
		c.Close()
		return nil, fmt.Errorf("IMAP-Begrüßung: %s", greet)
	}
	return ic, nil
}

func (ic *imapConn) Close() error { return ic.c.Close() }

// readLine liest eine Antwortzeile samt eingebetteter Literale ({n}\r\n<n Bytes>).
func (ic *imapConn) readLine() (string, [][]byte, error) {
	var sb strings.Builder
	var lits [][]byte
	for {
		line, err := ic.r.ReadString('\n')
		if err != nil {
			return "", nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		sb.WriteString(line)
		n, ok := literalSize(line)
		if !ok {
			return sb.String(), lits, nil
		}
		if n > 50<<20 {
			return "", nil, fmt.Errorf("Mail zu groß (%d Bytes)", n)
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(ic.r, buf); err != nil {
			return "", nil, err
		}
		lits = append(lits, buf)
	}
}

var reLiteral = regexp.MustCompile(`\{(\d+)\+?\}$`)

func literalSize(line string) (int, bool) {
	m := reLiteral.FindStringSubmatch(line)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

type imapResponse struct {
	text string
	lits [][]byte
}

// cmd sendet einen Befehl und sammelt die ungetaggten Antworten bis zur Statuszeile.
func (ic *imapConn) cmd(format string, args ...any) ([]imapResponse, error) {
	ic.tag++
	tag := fmt.Sprintf("b%d", ic.tag)
	if _, err := fmt.Fprintf(ic.c, "%s %s\r\n", tag, fmt.Sprintf(format, args...)); err != nil {
		return nil, err
	}
	var out []imapResponse
	for {
		text, lits, err := ic.readLine()
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(text, tag+" ") {
			status := strings.TrimPrefix(text, tag+" ")
			if !strings.HasPrefix(status, "OK") {
				return out, fmt.Errorf("IMAP: %s", status)
			}
			return out, nil
		}
		out = append(out, imapResponse{text, lits})
	}
}

func imapQuote(s string) (string, error) {
	if strings.ContainsAny(s, "\r\n\x00") {
		return "", errors.New("ungültige Zeichen in IMAP-Zugangsdaten")
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`, nil
}

var reUIDValidity = regexp.MustCompile(`\[UIDVALIDITY (\d+)\]`)

func (ic *imapConn) login(user, pass string) error {
	u, err := imapQuote(user)
	if err != nil {
		return err
	}
	p, err := imapQuote(pass)
	if err != nil {
		return err
	}
	_, err = ic.cmd("LOGIN %s %s", u, p)
	if err != nil {
		return errors.New("IMAP-Anmeldung fehlgeschlagen")
	}
	return nil
}

func (ic *imapConn) examine(mailbox string) (uint32, error) {
	mb, err := imapQuote(mailbox)
	if err != nil {
		return 0, err
	}
	resp, err := ic.cmd("EXAMINE %s", mb)
	if err != nil {
		return 0, err
	}
	for _, r := range resp {
		if m := reUIDValidity.FindStringSubmatch(r.text); m != nil {
			v, _ := strconv.ParseUint(m[1], 10, 32)
			return uint32(v), nil
		}
	}
	return 0, errors.New("Server meldet keine UIDVALIDITY")
}

func (ic *imapConn) searchSince(lastUID uint32) ([]uint32, error) {
	resp, err := ic.cmd("UID SEARCH UID %d:*", lastUID+1)
	if err != nil {
		return nil, err
	}
	var uids []uint32
	for _, r := range resp {
		if !strings.HasPrefix(r.text, "* SEARCH") {
			continue
		}
		for _, f := range strings.Fields(strings.TrimPrefix(r.text, "* SEARCH")) {
			// „n:*“ liefert die höchste UID auch dann, wenn sie ≤ n ist – deshalb filtern.
			if v, err := strconv.ParseUint(f, 10, 32); err == nil && uint32(v) > lastUID {
				uids = append(uids, uint32(v))
			}
		}
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	return uids, nil
}

func (ic *imapConn) fetchRaw(uid uint32) ([]byte, error) {
	resp, err := ic.cmd("UID FETCH %d (BODY.PEEK[])", uid)
	if err != nil {
		return nil, err
	}
	for _, r := range resp {
		if strings.Contains(r.text, "FETCH") && len(r.lits) > 0 {
			return r.lits[0], nil
		}
	}
	return nil, fmt.Errorf("UID %d: keine Nachricht erhalten", uid)
}

// fetchMail liest neue Alert-Mails und speichert die Inserate.
func fetchMail(ctx context.Context, cfg Config, store *Store) (res FetchResult) {
	res.Started = time.Now()
	defer func() {
		res.Finished = time.Now()
		if err := store.LogFetch(res); err != nil {
			log.Printf("Abruf-Protokoll: %v", err)
		}
	}()
	ic, err := connectIMAP(ctx, cfg.IMAPAddr)
	if err != nil {
		res.Err = fmt.Errorf("Verbindung zu %s: %w", cfg.IMAPAddr, err)
		return
	}
	defer func() {
		ic.cmd("LOGOUT")
		ic.Close()
	}()
	if res.Err = ic.login(cfg.IMAPUser, cfg.IMAPPassword); res.Err != nil {
		return
	}
	validity, err := ic.examine(cfg.IMAPMailbox)
	if err != nil {
		res.Err = err
		return
	}
	known, lastUID, err := store.MailState(cfg.IMAPMailbox)
	if err != nil {
		res.Err = err
		return
	}
	if known != validity {
		lastUID = 0 // Postfach neu nummeriert: alles noch einmal lesen, Upsert verhindert Dubletten.
	}
	uids, err := ic.searchSince(lastUID)
	if err != nil {
		res.Err = err
		return
	}
	if len(uids) > maxMessagesPerRun {
		uids = uids[:maxMessagesPerRun]
	}
	for _, uid := range uids {
		if ctx.Err() != nil {
			res.Err = ctx.Err()
			return
		}
		raw, err := ic.fetchRaw(uid)
		if err != nil {
			res.Err = err
			return
		}
		total, fresh, err := importMessage(store, raw)
		if err != nil {
			// Eine kaputte Mail soll den Abruf nicht dauerhaft blockieren.
			log.Printf("Mail UID %d: %v", uid, err)
		}
		res.Messages++
		res.New += fresh
		res.Updated += total - fresh
		if err := store.SetMailState(cfg.IMAPMailbox, validity, uid); err != nil {
			res.Err = err
			return
		}
	}
	if len(uids) == 0 && known != validity {
		res.Err = store.SetMailState(cfg.IMAPMailbox, validity, lastUID)
	}
	return
}
