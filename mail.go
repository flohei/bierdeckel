package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"unicode/utf8"
)

// Message ist eine dekodierte E-Mail mit den Teilen, die der Parser braucht.
type Message struct {
	Subject string
	From    string
	HTML    string
	Text    string
}

// decodeMessage zerlegt eine RFC-5322-Mail (auch verschachteltes Multipart)
// in Betreff, Absender sowie HTML- und Textteil.
func decodeMessage(raw []byte) (Message, error) {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return Message{}, err
	}
	dec := mime.WordDecoder{CharsetReader: charsetReader}
	var msg Message
	msg.Subject, _ = dec.DecodeHeader(m.Header.Get("Subject"))
	msg.From, _ = dec.DecodeHeader(m.Header.Get("From"))
	err = walkPart(textprotoHeader(m.Header), m.Body, &msg, 0)
	return msg, err
}

type header interface{ Get(string) string }

func textprotoHeader(h mail.Header) header { return h }

func walkPart(h header, body io.Reader, msg *Message, depth int) error {
	if depth > 10 {
		return nil
	}
	ct := h.Get("Content-Type")
	if ct == "" {
		ct = "text/plain; charset=us-ascii"
	}
	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil {
		mediaType, params = "text/plain", map[string]string{}
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		mr := multipart.NewReader(body, params["boundary"])
		for {
			p, err := mr.NextRawPart()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			if err := walkPart(p.Header, p, msg, depth+1); err != nil {
				return err
			}
		}
	}
	if !strings.HasPrefix(mediaType, "text/") {
		return nil
	}
	if strings.HasPrefix(strings.ToLower(h.Get("Content-Disposition")), "attachment") {
		return nil
	}
	data, err := io.ReadAll(transferDecoder(h.Get("Content-Transfer-Encoding"), body))
	if err != nil {
		return err
	}
	text, err := toUTF8(params["charset"], data)
	if err != nil {
		return err
	}
	switch mediaType {
	case "text/html":
		if msg.HTML == "" {
			msg.HTML = text
		}
	case "text/plain":
		if msg.Text == "" {
			msg.Text = text
		}
	}
	return nil
}

func transferDecoder(enc string, r io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(enc)) {
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, &stripSpace{r: r})
	}
	return r
}

// stripSpace entfernt Zeilenumbrüche aus Base64-Daten.
type stripSpace struct{ r io.Reader }

func (s *stripSpace) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	j := 0
	for i := 0; i < n; i++ {
		if c := p[i]; c != '\r' && c != '\n' && c != ' ' && c != '\t' {
			p[j] = c
			j++
		}
	}
	return j, err
}

// cp1252 belegt 0x80–0x9F anders als ISO-8859-1 (u. a. das €-Zeichen bei 0x80).
var cp1252 = [32]rune{
	'€', 0xFFFD, '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', 0xFFFD, 'Ž', 0xFFFD,
	0xFFFD, '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', 0xFFFD, 'ž', 'Ÿ',
}

func toUTF8(charset string, b []byte) (string, error) {
	switch strings.ToLower(strings.Trim(charset, `" `)) {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		if !utf8.Valid(b) {
			return latin1(b), nil
		}
		return string(b), nil
	case "iso-8859-1", "iso-8859-15", "latin1", "windows-1252", "cp1252":
		return latin1(b), nil
	}
	return "", fmt.Errorf("Zeichensatz %q wird nicht unterstützt", charset)
}

func latin1(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		if c >= 0x80 && c < 0xA0 {
			sb.WriteRune(cp1252[c-0x80])
		} else {
			sb.WriteRune(rune(c))
		}
	}
	return sb.String()
}

func charsetReader(charset string, input io.Reader) (io.Reader, error) {
	b, err := io.ReadAll(input)
	if err != nil {
		return nil, err
	}
	s, err := toUTF8(charset, b)
	if err != nil {
		return nil, err
	}
	return strings.NewReader(s), nil
}
