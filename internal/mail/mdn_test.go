package mail

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-message"
	gomail "github.com/emersion/go-message/mail"
)

func TestParsePriority(t *testing.T) {
	for _, c := range []struct {
		xp, imp string
		want    int
	}{
		{"1 (Highest)", "", 1}, {"2", "", 1}, {"3 (Normal)", "high", 0}, {"5", "", 5}, {"", "High", 1},
		{"", "low", 5}, {"", "", 0}, {"x", "low", 5},
	} {
		if got := ParsePriority(c.xp, c.imp); got != c.want {
			t.Errorf("ParsePriority(%q, %q) = %d, want %d", c.xp, c.imp, got, c.want)
		}
	}
	if priorityFromHeader([]byte("X-Priority: 5\r\nImportance: high\r\n")) != 5 {
		t.Error("header section")
	}
}

func TestBuildHeadersPriorityAndReceipt(t *testing.T) {
	raw, _, err := Build(&Draft{From: Address{Email: "bob@test.local"}, To: []Address{{Email: "a@test.local"}},
		Subject: "s", Text: "x", Priority: 1, ReadReceipt: true, DSN: true})
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{"X-Priority: 1 (Highest)", "Importance: high", "Disposition-Notification-To: <bob@test.local>"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if strings.Contains(s, "X-Webmail-DSN") {
		t.Error("DSN marker must stay only in drafts")
	}
}

func TestBuildMDN(t *testing.T) {
	raw, err := BuildMDN(MDN{From: Address{Email: "bob@test.local"}, To: "alice@test.local", Subject: "Привет",
		MessageID: "abc@test.local", Date: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	e, err := message.Read(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	ct, params, _ := e.Header.ContentType()
	if ct != "multipart/report" || params["report-type"] != "disposition-notification" {
		t.Fatalf("content-type %s %v", ct, params)
	}
	if e.Header.Get("Auto-Submitted") != "" {
		t.Error("manual MDN must not be marked auto-submitted")
	}
	mr := e.MultipartReader()
	var parts []string
	var report string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		pct, _, _ := p.Header.ContentType()
		parts = append(parts, pct)
		b, _ := io.ReadAll(p.Body)
		if pct == "message/disposition-notification" {
			report = string(b)
		}
	}
	if len(parts) != 2 || parts[0] != "text/plain" {
		t.Fatalf("parts: %v", parts)
	}
	for _, want := range []string{"Final-Recipient: rfc822;bob@test.local", "Original-Message-ID: <abc@test.local>",
		"Disposition: manual-action/MDN-sent-manually; displayed"} {
		if !strings.Contains(report, want) {
			t.Errorf("report misses %q:\n%s", want, report)
		}
	}
}

func TestBuildMDNLang(t *testing.T) {
	for lang, want := range map[string][2]string{
		"":   {"Read: Hi", "was displayed to the recipient"},
		"ru": {"Прочитано: Hi", "было показано получателю"},
	} {
		raw, err := BuildMDN(MDN{From: Address{Email: "bob@test.local"}, To: "alice@test.local", Subject: "Hi",
			Date: time.Now(), Lang: lang})
		if err != nil {
			t.Fatal(err)
		}
		e, err := message.Read(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if subj, _ := (&gomail.Header{Header: e.Header}).Subject(); subj != want[0] {
			t.Errorf("lang %q: subject %q", lang, subj)
		}
		p, err := e.MultipartReader().NextPart()
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(p.Body)
		if !strings.Contains(string(body), want[1]) {
			t.Errorf("lang %q: body %q", lang, body)
		}
	}
}
