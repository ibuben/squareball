package mail

import (
	"bytes"
	"io"
	"strings"
	"testing"

	gomail "github.com/emersion/go-message/mail"
)

func TestBuildHidesBccAndKeepsAttachments(t *testing.T) {
	d := &Draft{
		From:        Address{Name: "Иван", Email: "ivan@example.com"},
		To:          []Address{{Email: "a@example.com"}},
		Cc:          []Address{{Email: "b@example.com"}},
		Bcc:         []Address{{Email: "secret@example.com"}},
		Subject:     "Тема письма",
		HTML:        "<p>Привет, <b>мир</b></p>",
		InReplyTo:   "orig@example.com",
		References:  []string{"root@example.com"},
		Attachments: []Upload{{Filename: "отчёт.pdf", ContentType: "application/pdf", Data: []byte("%PDF-1.4")}},
	}
	raw, msgID, err := Build(d)
	if err != nil {
		t.Fatal(err)
	}
	if msgID == "" || !strings.HasSuffix(msgID, "@example.com") {
		t.Errorf("message-id = %q", msgID)
	}
	if bytes.Contains(raw, []byte("secret@example.com")) {
		t.Fatal("Bcc leaked into headers")
	}
	rcpts := d.Recipients()
	if len(rcpts) != 3 || rcpts[2] != "secret@example.com" {
		t.Errorf("recipients = %v", rcpts)
	}

	mr, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := mr.Header.Subject(); s != "Тема письма" {
		t.Errorf("subject = %q", s)
	}
	if refs, _ := mr.Header.MsgIDList("References"); len(refs) != 2 || refs[1] != "orig@example.com" {
		t.Errorf("references = %v", refs)
	}
	var text, html, att string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(p.Body)
		switch h := p.Header.(type) {
		case *gomail.InlineHeader:
			ct, _, _ := h.ContentType()
			if ct == "text/html" {
				html = string(b)
			} else {
				text = string(b)
			}
		case *gomail.AttachmentHeader:
			att, _ = h.Filename()
		}
	}
	if !strings.Contains(html, "<b>мир</b>") || !strings.Contains(text, "Привет, мир") || att != "отчёт.pdf" {
		t.Errorf("parts: text=%q html=%q att=%q", text, html, att)
	}
}

func TestBuildPlainText(t *testing.T) {
	raw, _, err := Build(&Draft{From: Address{Email: "a@b.c"}, To: []Address{{Email: "d@e.f"}}, Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte("Content-Type: text/plain")) || bytes.Contains(raw, []byte("multipart")) {
		t.Errorf("want single text part:\n%s", raw)
	}
}

func TestRecipientsDeduplicated(t *testing.T) {
	d := &Draft{To: []Address{{Email: "A@x.y"}}, Cc: []Address{{Email: "a@x.y"}, {Email: ""}}}
	if r := d.Recipients(); len(r) != 1 {
		t.Errorf("recipients = %v", r)
	}
}
