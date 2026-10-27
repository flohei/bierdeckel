package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
)

// fakeIMAP ist ein winziger IMAP-Server für Tests: ein Postfach, nur die Befehle des Clients.
type fakeIMAP struct {
	t           *testing.T
	uidvalidity int
	msgs        map[uint32][]byte
	commands    []string
}

func (f *fakeIMAP) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	fmt.Fprint(c, "* OK fake ready\r\n")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		f.commands = append(f.commands, line)
		tag, rest, _ := strings.Cut(line, " ")
		switch {
		case strings.HasPrefix(rest, "LOGIN"):
			if rest != `LOGIN "alerts@example.org" "ge\"heim"` {
				fmt.Fprintf(c, "%s NO falsch\r\n", tag)
				continue
			}
			fmt.Fprintf(c, "%s OK\r\n", tag)
		case strings.HasPrefix(rest, "EXAMINE"):
			fmt.Fprintf(c, "* %d EXISTS\r\n* OK [UIDVALIDITY %d] ok\r\n%s OK [READ-ONLY]\r\n", len(f.msgs), f.uidvalidity, tag)
		case strings.HasPrefix(rest, "UID SEARCH"):
			var from uint32
			fmt.Sscanf(rest, "UID SEARCH UID %d:*", &from)
			var uids []string
			var maxUID uint32
			for u := range f.msgs {
				if u >= from {
					uids = append(uids, fmt.Sprint(u))
				}
				maxUID = max(maxUID, u)
			}
			if len(uids) == 0 && maxUID > 0 {
				uids = append(uids, fmt.Sprint(maxUID)) // RFC-Verhalten von n:*
			}
			fmt.Fprintf(c, "* SEARCH %s\r\n%s OK\r\n", strings.Join(uids, " "), tag)
		case strings.HasPrefix(rest, "UID FETCH"):
			var uid uint32
			fmt.Sscanf(rest, "UID FETCH %d", &uid)
			m := f.msgs[uid]
			fmt.Fprintf(c, "* 1 FETCH (UID %d BODY[] {%d}\r\n", uid, len(m))
			c.Write(m)
			fmt.Fprintf(c, ")\r\n%s OK\r\n", tag)
		case rest == "LOGOUT":
			fmt.Fprintf(c, "* BYE\r\n%s OK\r\n", tag)
			return
		default:
			fmt.Fprintf(c, "%s BAD\r\n", tag)
		}
	}
}

func withFakeIMAP(t *testing.T, f *fakeIMAP) {
	old := dialIMAP
	dialIMAP = func(ctx context.Context, addr string) (net.Conn, error) {
		client, server := net.Pipe()
		go f.serve(server)
		return client, nil
	}
	t.Cleanup(func() { dialIMAP = old })
}

func TestFetchMail(t *testing.T) {
	is24, _ := os.ReadFile("testdata/is24-synthetisch.eml")
	iw, _ := os.ReadFile("testdata/immowelt-synthetisch.eml")
	f := &fakeIMAP{t: t, uidvalidity: 7, msgs: map[uint32][]byte{3: is24, 5: iw, 6: []byte("kaputt")}}
	withFakeIMAP(t, f)
	s := newTestStore(t)
	cfg := Config{IMAPAddr: "imap.example.org:993", IMAPUser: "alerts@example.org", IMAPPassword: `ge"heim`, IMAPMailbox: "INBOX"}

	res := fetchMail(context.Background(), cfg, s)
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if res.Messages != 3 || res.New != 4 {
		t.Errorf("1. Abruf: %s", res.Summary())
	}
	if _, last, _ := s.MailState("INBOX"); last != 6 {
		t.Errorf("letzte UID %d", last)
	}

	// Zweiter Abruf: nichts Neues, obwohl der Server für „7:*“ die UID 6 liefert.
	res = fetchMail(context.Background(), cfg, s)
	if res.Err != nil || res.Messages != 0 {
		t.Errorf("2. Abruf: %s", res.Summary())
	}

	// Neue Nachricht mit bereits bekanntem Inserat → Aktualisierung, kein neues Inserat.
	f.msgs[9] = is24
	res = fetchMail(context.Background(), cfg, s)
	if res.Err != nil || res.Messages != 1 || res.New != 0 || res.Updated != 2 {
		t.Errorf("3. Abruf: %s", res.Summary())
	}
	for _, c := range f.commands {
		if strings.Contains(c, "STORE") || strings.Contains(c, "SELECT") || (strings.Contains(c, "FETCH") && !strings.Contains(c, "BODY.PEEK")) {
			t.Errorf("Postfach darf nicht verändert werden: %s", c)
		}
	}
	if logs, _ := s.RecentFetches(10); len(logs) != 3 {
		t.Errorf("%d Protokolleinträge", len(logs))
	}
}

func TestFetchMailBadLogin(t *testing.T) {
	withFakeIMAP(t, &fakeIMAP{t: t, uidvalidity: 1, msgs: map[uint32][]byte{}})
	s := newTestStore(t)
	res := fetchMail(context.Background(), Config{IMAPAddr: "x:993", IMAPUser: "falsch", IMAPPassword: "x", IMAPMailbox: "INBOX"}, s)
	if res.Err == nil || !strings.Contains(res.Err.Error(), "Anmeldung") {
		t.Errorf("Fehler erwartet: %v", res.Err)
	}
	// Das Passwort darf nicht im Protokoll landen.
	logs, _ := s.RecentFetches(1)
	if len(logs) != 1 || strings.Contains(logs[0].ErrText, "falsch") {
		t.Errorf("Protokoll: %+v", logs)
	}
}

func TestUIDValidityChangeRereads(t *testing.T) {
	is24, _ := os.ReadFile("testdata/is24-synthetisch.eml")
	f := &fakeIMAP{t: t, uidvalidity: 1, msgs: map[uint32][]byte{10: is24}}
	withFakeIMAP(t, f)
	s := newTestStore(t)
	cfg := Config{IMAPAddr: "x:993", IMAPUser: "alerts@example.org", IMAPPassword: `ge"heim`, IMAPMailbox: "INBOX"}
	fetchMail(context.Background(), cfg, s)
	f.uidvalidity = 2
	f.msgs = map[uint32][]byte{1: is24}
	res := fetchMail(context.Background(), cfg, s)
	if res.Err != nil || res.Messages != 1 || res.New != 0 {
		t.Errorf("nach UIDVALIDITY-Wechsel: %s", res.Summary())
	}
}
